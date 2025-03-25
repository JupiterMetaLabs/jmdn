# Blockchain Explorer API Endpoints for Postman

Here are all the API endpoints you can test in Postman. Assuming your server is running at `http://localhost:8085`:

## Dashboard & Stats

1. **Get Dashboard Data**:

   ```
   http://localhost:8085/api/dashboard
   ```

   Method: GET
2. **Get Statistics**:

   ```
   http://localhost:8085/api/stats
   ```

   Method: GET

## Blocks

3. **List Blocks** (with pagination):

   ```
   http://localhost:8085/api/blocks?offset=0&limit=10
   ```

   Method: GET

   Parameters:

   - `offset` (optional): Starting position (default: 0)
   - `limit` (optional): Number of blocks to return (default: 10)
4. **Get Block by ID**:

   ```
   http://localhost:8085/api/blocks/{id}
   ```

   Method: GET

   Example:

   ```
   http://localhost:8085/api/blocks/abc123
   ```

   Replace `abc123` with an actual block ID from your system.

## Transactions

5. **List Transactions** (with pagination):

   ```
   http://localhost:8085/api/transactions?offset=0&limit=10
   ```

   Method: GET

   Parameters:

   - `offset` (optional): Starting position (default: 0)
   - `limit` (optional): Number of transactions to return (default: 10)
6. **Get Transaction by Hash**:

   ```
   http://localhost:8085/api/transactions/{hash}
   ```

   Method: GET

   Example:

   ```
   http://localhost:8085/api/transactions/0x1234abcd
   ```

   Replace `0x1234abcd` with an actual transaction hash from your system.

## Other Endpoints

7. **Health Check**:

   ```
   http://localhost:8085/api/health
   ```

   Method: GET
8. **WebSocket Connection** (not for Postman, but for WebSocket clients):

   ```
   ws://localhost:8085/ws
   ```

You can import these as a collection in Postman to test them all. Let me know if you need help with any specific endpoint!
