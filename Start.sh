docker-compose up -d
# go run . -heartbeat 10 -metrics 8080 -api 8090 -blockgen 15050 -mempool localhost:15051 -did localhost:15052 -cli 15053 -seednode 34.174.233.203:17002
go run . -heartbeat 10 -metrics 8080 -api 8090 -blockgen 15050 -did localhost:15052 -cli 15053 -seednode localhost:17003 -facade 8081 -ws 8086