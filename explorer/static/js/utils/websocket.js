class WebSocketClient {
    constructor(url, onMessage, onConnect, onDisconnect) {
      this.url = url;
      this.onMessage = onMessage;
      this.onConnect = onConnect;
      this.onDisconnect = onDisconnect;
      this.socket = null;
      this.reconnectTimeout = null;
      this.reconnectAttempts = 0;
      this.maxReconnectAttempts = 5;
      this.reconnectDelay = 2000; // Start with 2 second delay
    }
    
    connect() {
      try {
        this.socket = new WebSocket(this.url);
        
        this.socket.onopen = () => {
          console.log('WebSocket connected');
          this.reconnectAttempts = 0;
          if (this.onConnect) this.onConnect();
        };
        
        this.socket.onmessage = (event) => {
          try {
            const data = JSON.parse(event.data);
            if (this.onMessage) this.onMessage(data);
          } catch (e) {
            console.error('Error parsing WebSocket message', e);
          }
        };
        
        this.socket.onclose = (event) => {
          console.log('WebSocket disconnected', event.code, event.reason);
          if (this.onDisconnect) this.onDisconnect();
          this.reconnect();
        };
        
        this.socket.onerror = (error) => {
          console.error('WebSocket error', error);
        };
      } catch (error) {
        console.error('Error creating WebSocket', error);
        this.reconnect();
      }
    }
    
    reconnect() {
      if (this.reconnectAttempts >= this.maxReconnectAttempts) {
        console.error('Max reconnect attempts reached');
        return;
      }
      
      this.reconnectAttempts++;
      const delay = this.reconnectDelay * Math.pow(1.5, this.reconnectAttempts - 1);
      
      console.log(`Attempting to reconnect in ${delay}ms (attempt ${this.reconnectAttempts})`);
      
      clearTimeout(this.reconnectTimeout);
      this.reconnectTimeout = setTimeout(() => {
        this.connect();
      }, delay);
    }
    
    disconnect() {
      if (this.socket) {
        this.socket.close();
        this.socket = null;
      }
      
      clearTimeout(this.reconnectTimeout);
    }
    
    send(data) {
      if (this.socket && this.socket.readyState === WebSocket.OPEN) {
        this.socket.send(JSON.stringify(data));
      } else {
        console.error('Cannot send message, WebSocket is not connected');
      }
    }
  }