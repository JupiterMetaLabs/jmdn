docker-compose up -d
# go run . -heartbeat 10 -metrics 8080 -api 8090 -blockgen 15050 -mempool localhost:15051 -did localhost:15052 -cli 15053 -seednode 34.174.233.203:17002
go run . -heartbeat 10 -metrics 8080 -api 8090 -blockgen 15050 -did localhost:15052 -cli 15053 -seednode 34.174.233.203:17002