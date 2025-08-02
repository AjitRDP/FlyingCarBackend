# Flying Car Game - Backend Server

WebSocket server for the multiplayer flying car game built with Go and Gorilla WebSocket.

## 🚀 Quick Start

```bash
# Install dependencies
go mod tidy

# Run the server
go run main.go
```

Or use the provided scripts:

**Windows:**
```bash
.\run.bat
```

**macOS/Linux:**
```bash
chmod +x run.sh
./run.sh
```

## 🌐 API Endpoints

### WebSocket Connection
- **URL**: `ws://localhost:8080/ws`
- **Protocol**: WebSocket with JSON messages

### HTTP Health Check
- **URL**: `http://localhost:8080/`
- **Method**: GET
- **Response**: "Flying Car Game Server Running"

## 📡 WebSocket Message Protocol

### Client → Server Messages

#### Update Position
```json
{
  "type": "updatePosition",
  "data": {
    "position": {"x": 0.0, "y": 1.0, "z": 0.0},
    "rotation": {"x": 0.0, "y": 0.0, "z": 0.0}
  }
}
```

### Server → Client Messages

#### Initial Connection
```json
{
  "type": "init",
  "playerId": "player-abc123",
  "data": {
    "yourId": "player-abc123",
    "yourColor": "#ff4444",
    "yourName": "Player abc123",
    "allPlayers": [...]
  }
}
```

#### New Player Joined
```json
{
  "type": "newPlayer",
  "playerId": "player-def456",
  "data": {
    "id": "player-def456",
    "name": "Player def456",
    "position": {"x": 0.0, "y": 1.0, "z": 0.0},
    "rotation": {"x": 0.0, "y": 0.0, "z": 0.0},
    "color": "#44ff44"
  }
}
```

#### Player Position Update
```json
{
  "type": "playerUpdate",
  "playerId": "player-abc123",
  "data": {
    "position": {"x": 0.0, "y": 1.0, "z": 0.0},
    "rotation": {"x": 0.0, "y": 0.0, "z": 0.0}
  }
}
```

#### Player Disconnected
```json
{
  "type": "playerDisconnected",
  "playerId": "player-abc123",
  "data": {}
}
```

## 🏗️ Architecture

### Core Components

- **GameServer**: Main server struct managing all players and game state
- **Player**: Represents a connected player with position, rotation, and metadata
- **Message**: Standard message format for WebSocket communication

### Key Features

- **Thread-safe**: Uses sync.RWMutex for concurrent player management
- **Auto-cleanup**: Automatically removes disconnected players
- **Broadcasting**: Efficient message distribution to all connected clients
- **CORS enabled**: Allows connections from any origin (development mode)

### Performance Characteristics

- **Concurrent connections**: Handles multiple players simultaneously
- **Memory efficient**: In-memory player state management
- **Low latency**: Direct WebSocket communication without additional queuing

## 🔧 Configuration

### Environment Variables

```bash
PORT=8080                    # Server port (default: 8080)
CORS_ORIGIN=*               # CORS origin (default: *, allows all)
MAX_PLAYERS=100             # Maximum concurrent players
UPDATE_RATE=30              # Server update rate (Hz)
```

### Server Limits

- **Max concurrent connections**: 1000 (adjustable)
- **Message size limit**: 1KB per message
- **Timeout**: 60 seconds for idle connections

## 🐛 Debugging

### Enable Debug Logging

```go
// Add to main.go
log.SetLevel(log.DebugLevel)
```

### Common Issues

1. **Port already in use**
   ```bash
   # Find process using port 8080
   netstat -ano | findstr :8080  # Windows
   lsof -i :8080                 # macOS/Linux
   ```

2. **WebSocket upgrade failed**
   - Check CORS settings
   - Verify client connection URL
   - Ensure proper HTTP headers

3. **Memory leaks**
   - Check for unclosed WebSocket connections
   - Monitor goroutine count: `go tool pprof`

## 📊 Monitoring

### Basic Metrics

The server logs the following metrics:
- Player connections/disconnections
- Total active players
- Message processing errors
- Connection failures

### Production Monitoring

For production deployment, consider adding:
- Prometheus metrics
- Health check endpoints
- Structured logging (JSON)
- Performance profiling

## 🚀 Production Deployment

### Build for Production

```bash
# Build optimized binary
go build -ldflags="-w -s" -o flying-car-server main.go

# Run with production settings
./flying-car-server
```

### Docker Deployment

```dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o server main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/server .
EXPOSE 8080
CMD ["./server"]
```

### Systemd Service (Linux)

```ini
[Unit]
Description=Flying Car Game Server
After=network.target

[Service]
Type=simple
User=gameserver
WorkingDirectory=/opt/flying-car-game
ExecStart=/opt/flying-car-game/server
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## 🔒 Security Considerations

### Development vs Production

**Development (current setup):**
- CORS allows all origins (`*`)
- No authentication required
- No rate limiting
- Debug logging enabled

**Production recommendations:**
- Restrict CORS to specific domains
- Implement player authentication
- Add rate limiting per connection
- Use secure WebSocket (WSS)
- Implement input validation
- Add DDoS protection

### Input Validation

```go
// Example validation for position updates
func validatePosition(pos map[string]float64) bool {
    x, y, z := pos["x"], pos["y"], pos["z"]
    return math.Abs(x) < 1000 && y >= 0 && y < 1000 && math.Abs(z) < 1000
}
```

## 📈 Scaling

### Horizontal Scaling

For multiple server instances:
- Use Redis for shared player state
- Implement load balancing
- Add sticky sessions for WebSocket connections

### Vertical Scaling

- Increase Go's GOMAXPROCS
- Tune garbage collector settings
- Optimize JSON marshaling/unmarshaling

## 🧪 Testing

### Unit Tests

```bash
go test ./...
```

### Load Testing

```bash
# Install websocket load testing tool
npm install -g artillery

# Run load test
artillery quick --count 100 --num 10 ws://localhost:8080/ws
``` 