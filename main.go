package main

import (
	"crypto/rand"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// Allow connections from any origin (for development)
		// Log the origin for debugging
		origin := r.Header.Get("Origin")
		log.Printf("🌐 WebSocket upgrade request from origin: %s", origin)
		return true
	},
	// Add additional headers for better browser compatibility
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// ConnectionMonitor tracks active connections to prevent overload
type ConnectionMonitor struct {
	activeConnections map[string]*ConnectionInfo
	maxConnections    int
	mutex             sync.RWMutex
}

type ConnectionInfo struct {
	conn         *websocket.Conn
	roomID       string
	connectedAt  time.Time
	lastActivity time.Time
}

var connectionMonitor = &ConnectionMonitor{
	activeConnections: make(map[string]*ConnectionInfo),
	maxConnections:    1000, // Configurable limit
}

func (cm *ConnectionMonitor) AddConnection(playerID string, conn *websocket.Conn, roomID string) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	
	cm.activeConnections[playerID] = &ConnectionInfo{
		conn:         conn,
		roomID:       roomID,
		connectedAt:  time.Now(),
		lastActivity: time.Now(),
	}
	
	usage := (len(cm.activeConnections) * 100) / cm.maxConnections
	log.Printf("📊 Active connections: %d/%d (%d%%)", len(cm.activeConnections), cm.maxConnections, usage)
	
	if usage > 80 {
		log.Printf("⚠️ Connection pool at %d%% capacity", usage)
	}
}

func (cm *ConnectionMonitor) RemoveConnection(playerID string) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	
	if connInfo, exists := cm.activeConnections[playerID]; exists {
		// Force close WebSocket if still open
		if connInfo.conn != nil {
			connInfo.conn.Close()
		}
		delete(cm.activeConnections, playerID)
		log.Printf("🔌 Connection %s removed. Active: %d", playerID, len(cm.activeConnections))
	}
}

func (cm *ConnectionMonitor) UpdateActivity(playerID string) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()
	
	if connInfo, exists := cm.activeConnections[playerID]; exists {
		connInfo.lastActivity = time.Now()
	}
}

func (cm *ConnectionMonitor) GetStats() map[string]interface{} {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()
	
	usage := (len(cm.activeConnections) * 100) / cm.maxConnections
	return map[string]interface{}{
		"active": len(cm.activeConnections),
		"max":    cm.maxConnections,
		"usage":  usage,
	}
}

func (cm *ConnectionMonitor) CleanupStaleConnections() {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	
	now := time.Now()
	staleThreshold := 30 * time.Second
	var staleConnections []string
	
	for playerID, connInfo := range cm.activeConnections {
		if now.Sub(connInfo.lastActivity) > staleThreshold {
			staleConnections = append(staleConnections, playerID)
		}
	}
	
	for _, playerID := range staleConnections {
		if connInfo := cm.activeConnections[playerID]; connInfo != nil {
			log.Printf("🧹 Force cleaning stale connection: %s", playerID)
			if connInfo.conn != nil {
				connInfo.conn.Close()
			}
			delete(cm.activeConnections, playerID)
		}
	}
	
	if len(staleConnections) > 0 {
		log.Printf("🧹 Force cleaned %d stale connections", len(staleConnections))
	}
}

func (cm *ConnectionMonitor) CheckCapacity() bool {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()
	
	usage := (len(cm.activeConnections) * 100) / cm.maxConnections
	return usage < 95 // Reject connections at 95% capacity
}

// Player represents a connected player
type Player struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Position    map[string]float64     `json:"position"`
	Rotation    map[string]float64     `json:"rotation"`
	Color       string                 `json:"color"`
	RoomID      string                 `json:"roomId"`
	Conn        *websocket.Conn        `json:"-"`
	LastSeen    time.Time              `json:"lastSeen"`
	IsConnected bool                   `json:"isConnected"`
}

// SafeClose safely closes a WebSocket connection with proper error handling
func (p *Player) SafeClose() {
	if p.Conn != nil {
		// Try graceful close first
		err := p.Conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Player leaving room"))
		if err != nil {
			log.Printf("⚠️ Error sending close message for %s: %v", p.ID, err)
		}
		
		// Close the connection
		err = p.Conn.Close()
		if err != nil {
			log.Printf("⚠️ Error closing connection for %s: %v", p.ID, err)
		}
		
		p.Conn = nil
		log.Printf("🔒 Connection safely closed for player %s", p.ID)
	}
}

// Message represents WebSocket messages
type Message struct {
	Type     string                 `json:"type"`
	PlayerID string                 `json:"playerId"`
	Data     map[string]interface{} `json:"data"`
}

// Room represents a game room with its players
type Room struct {
	ID        string             `json:"id"`
	Players   map[string]*Player `json:"players"`
	CreatedAt time.Time          `json:"createdAt"`
	mutex     sync.RWMutex
}

// forceCleanupAllConnections forcefully closes all connections in the room
func (r *Room) forceCleanupAllConnections() {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	
	log.Printf("🧹 Force cleaning all connections in room %s", r.ID)
	for playerID, player := range r.Players {
		player.SafeClose()
		connectionMonitor.RemoveConnection(playerID)
	}
	r.Players = make(map[string]*Player) // Clear the map
}

// GameServer manages all rooms and game state
type GameServer struct {
	rooms map[string]*Room
	mutex sync.RWMutex
}

func NewGameServer() *GameServer {
	gs := &GameServer{
		rooms: make(map[string]*Room),
	}
	
	// Start cleanup routine for disconnected players
	go gs.cleanupDisconnectedPlayers()
	
	// Start connection monitoring
	go gs.startConnectionMonitoring()
	
	return gs
}

// startConnectionMonitoring monitors connection health
func (gs *GameServer) startConnectionMonitoring() {
	ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			connectionMonitor.CleanupStaleConnections()
			
			stats := connectionMonitor.GetStats()
			usage := stats["usage"].(int)
			
			log.Printf("📊 Connection Monitor: %d/%d (%d%%)", stats["active"], stats["max"], usage)
			
			if usage > 90 {
				log.Printf("🚨 CRITICAL: Connection pool at %d%% - Emergency cleanup!", usage)
				gs.emergencyCleanup()
			} else if usage > 70 {
				log.Printf("⚠️ Connection pool stressed (%d%%), running aggressive cleanup", usage)
				gs.aggressiveCleanup()
			}
		}
	}
}

// emergencyCleanup performs immediate cleanup when connection pool is critical
func (gs *GameServer) emergencyCleanup() {
	log.Printf("🚨 EMERGENCY CLEANUP INITIATED")
	
	gs.mutex.Lock()
	defer gs.mutex.Unlock()
	
	// Force close all connections in empty rooms
	for roomID, room := range gs.rooms {
		room.mutex.RLock()
		isEmpty := len(room.Players) == 0
		room.mutex.RUnlock()
		
		if isEmpty {
			room.forceCleanupAllConnections()
			delete(gs.rooms, roomID)
		}
	}
	
	connectionMonitor.CleanupStaleConnections()
	
	stats := connectionMonitor.GetStats()
	log.Printf("🚨 Emergency cleanup complete. Rooms: %d, Connections: %d", len(gs.rooms), stats["active"])
}

// aggressiveCleanup performs faster cleanup when connection pool is stressed
func (gs *GameServer) aggressiveCleanup() {
	now := time.Now()
	aggressiveTimeout := 2 * time.Minute // 2 minutes instead of 5
	cleanedPlayers := 0
	cleanedRooms := 0
	
	gs.mutex.RLock()
	roomsToClean := make([]string, 0, len(gs.rooms))
	for roomID := range gs.rooms {
		roomsToClean = append(roomsToClean, roomID)
	}
	gs.mutex.RUnlock()
	
	for _, roomID := range roomsToClean {
		gs.mutex.RLock()
		room, exists := gs.rooms[roomID]
		gs.mutex.RUnlock()
		
		if !exists {
			continue
		}
		
		room.mutex.Lock()
		playersToRemove := []string{}
		
		// More aggressive player cleanup
		for playerID, player := range room.Players {
			if !player.IsConnected && now.Sub(player.LastSeen) > aggressiveTimeout {
				playersToRemove = append(playersToRemove, playerID)
			}
		}
		
		for _, playerID := range playersToRemove {
			if player := room.Players[playerID]; player != nil {
				player.SafeClose()
				connectionMonitor.RemoveConnection(playerID)
				delete(room.Players, playerID)
				cleanedPlayers++
			}
		}
		
		// Remove inactive rooms more quickly
		roomAge := now.Sub(room.CreatedAt)
		isEmpty := len(room.Players) == 0
		
		room.mutex.Unlock()
		
		if isEmpty && roomAge > time.Minute {
			room.forceCleanupAllConnections()
			gs.mutex.Lock()
			delete(gs.rooms, roomID)
			gs.mutex.Unlock()
			cleanedRooms++
		}
	}
	
	log.Printf("🧹 Aggressive cleanup: %d players, %d rooms removed", cleanedPlayers, cleanedRooms)
}

// cleanupDisconnectedPlayers removes players who have been disconnected for too long
func (gs *GameServer) cleanupDisconnectedPlayers() {
	ticker := time.NewTicker(1 * time.Minute) // Check every minute
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			// Collect rooms to clean up first
			var roomsToClean []string
			gs.mutex.RLock()
			for roomID := range gs.rooms {
				roomsToClean = append(roomsToClean, roomID)
			}
			gs.mutex.RUnlock()
			
			// Clean each room individually
			var emptyRooms []string
			for _, roomID := range roomsToClean {
				gs.mutex.RLock()
				room, exists := gs.rooms[roomID]
				gs.mutex.RUnlock()
				
				if !exists {
					continue
				}
				
				room.mutex.Lock()
				playersToRemove := []string{}
				
				for playerID, player := range room.Players {
					// Remove players disconnected for more than 5 minutes
					if !player.IsConnected && time.Since(player.LastSeen) > 5*time.Minute {
						playersToRemove = append(playersToRemove, playerID)
					}
				}
				
				for _, playerID := range playersToRemove {
					delete(room.Players, playerID)
					log.Printf("🗑️ Cleaned up disconnected player %s from room %s", playerID, roomID)
				}
				
				// Check if room is empty after cleanup
				isEmpty := len(room.Players) == 0
				room.mutex.Unlock()
				
				if isEmpty {
					emptyRooms = append(emptyRooms, roomID)
				}
			}
			
			// Remove empty rooms
			if len(emptyRooms) > 0 {
				gs.mutex.Lock()
				for _, roomID := range emptyRooms {
					// Double-check the room is still empty and not too new
					if room, exists := gs.rooms[roomID]; exists {
						room.mutex.RLock()
						stillEmpty := len(room.Players) == 0
						roomAge := time.Since(room.CreatedAt)
						room.mutex.RUnlock()
						
						// Only remove rooms that are empty AND older than 2 minutes
						// This prevents cleanup of newly created rooms
						if stillEmpty && roomAge > 2*time.Minute {
							delete(gs.rooms, roomID)
							log.Printf("🗑️ Removed empty room: %s (age: %v)", roomID, roomAge)
						} else if stillEmpty {
							log.Printf("🕒 Keeping new empty room: %s (age: %v, too new to clean)", roomID, roomAge)
						}
					}
				}
				gs.mutex.Unlock()
				
				// Log cleanup summary
				gs.mutex.RLock()
				totalRooms := len(gs.rooms)
				gs.mutex.RUnlock()
				log.Printf("🧹 Cleanup complete. Rooms remaining: %d", totalRooms)
			}
		}
	}
}

// GetOrCreateRoom gets an existing room or creates a new one
func (gs *GameServer) GetOrCreateRoom(roomID string) *Room {
	gs.mutex.Lock()
	defer gs.mutex.Unlock()
	
	if room, exists := gs.rooms[roomID]; exists {
		return room
	}
	
	// Create new room
	room := &Room{
		ID:        roomID,
		Players:   make(map[string]*Player),
		CreatedAt: time.Now(),
	}
	gs.rooms[roomID] = room
	log.Printf("🏠 Created new room: %s at %v", roomID, room.CreatedAt.Format("15:04:05"))
	return room
}

// AddPlayer adds a new player to a specific room
func (gs *GameServer) AddPlayer(player *Player) {
	room := gs.GetOrCreateRoom(player.RoomID)
	room.mutex.Lock()
	defer room.mutex.Unlock()
	
	player.IsConnected = true
	player.LastSeen = time.Now()
	room.Players[player.ID] = player
	
	// Add to connection monitor
	connectionMonitor.AddConnection(player.ID, player.Conn, player.RoomID)
	
	log.Printf("➕ Added player %s (%s) to room %s. Room players: %d", player.Name, player.ID, player.RoomID, len(room.Players))
}

// ReconnectPlayer attempts to reconnect a player with their previous state
func (gs *GameServer) ReconnectPlayer(roomID, previousPlayerID string, conn *websocket.Conn) *Player {
	room := gs.GetOrCreateRoom(roomID)
	room.mutex.Lock()
	defer room.mutex.Unlock()
	
	// Check if previous player exists in this room
	if existingPlayer, exists := room.Players[previousPlayerID]; exists {
		// Update connection and status
		existingPlayer.Conn = conn
		existingPlayer.IsConnected = true
		existingPlayer.LastSeen = time.Now()
		
		log.Printf("🔄 Reconnected player %s (%s) to room %s. Position preserved.", existingPlayer.Name, previousPlayerID, roomID)
		return existingPlayer
	}
	
	return nil // Player not found for reconnection
}

// DisconnectPlayer marks a player as disconnected but keeps their state
func (gs *GameServer) DisconnectPlayer(playerID string, roomID string) {
	gs.mutex.RLock()
	room, roomExists := gs.rooms[roomID]
	gs.mutex.RUnlock()
	
	if !roomExists {
		log.Printf("⚠️ Attempted to disconnect player from non-existent room: %s", roomID)
		return
	}
	
	room.mutex.Lock()
	defer room.mutex.Unlock()
	
	if player, exists := room.Players[playerID]; exists {
		// Safe connection cleanup
		player.SafeClose()
		
		// Remove from connection monitor
		connectionMonitor.RemoveConnection(playerID)
		
		// Mark as disconnected but keep player data
		player.IsConnected = false
		player.LastSeen = time.Now()
		
		log.Printf("🚪 Player %s (%s) disconnected from room %s. State preserved for reconnection.", player.Name, playerID, roomID)
		
		// Notify other players about disconnection
		disconnectMsg := Message{
			Type:     "playerDisconnected",
			PlayerID: playerID,
			Data:     map[string]interface{}{"id": playerID},
		}
		gs.BroadcastToRoom(disconnectMsg, roomID, playerID)
		log.Printf("📢 Notified other players in room %s about %s disconnecting", roomID, player.Name)
	} else {
		log.Printf("⚠️ Attempted to disconnect non-existent player: %s from room: %s", playerID, roomID)
	}
}

// RemovePlayer completely removes a player from their room (for cleanup)
func (gs *GameServer) RemovePlayer(playerID string, roomID string) {
	gs.mutex.RLock()
	room, roomExists := gs.rooms[roomID]
	gs.mutex.RUnlock()
	
	if !roomExists {
		return
	}
	
	room.mutex.Lock()
	defer room.mutex.Unlock()
	
	if player, exists := room.Players[playerID]; exists {
		if player.Conn != nil {
			player.Conn.Close()
		}
		delete(room.Players, playerID)
		log.Printf("🗑️ Permanently removed player %s (%s) from room %s", player.Name, playerID, roomID)
	}
}

// UpdatePlayer updates a player's position and rotation in their room
func (gs *GameServer) UpdatePlayer(playerID string, roomID string, position, rotation map[string]float64) {
	gs.mutex.RLock()
	room, roomExists := gs.rooms[roomID]
	gs.mutex.RUnlock()
	
	if !roomExists {
		return
	}
	
	room.mutex.Lock()
	defer room.mutex.Unlock()
	
	if player, exists := room.Players[playerID]; exists && player.IsConnected {
		player.Position = position
		player.Rotation = rotation
		player.LastSeen = time.Now()
	}
}

// BroadcastToRoom sends a message to all connected players in a room except the sender
func (gs *GameServer) BroadcastToRoom(message Message, roomID string, excludePlayerID string) {
	gs.mutex.RLock()
	room, roomExists := gs.rooms[roomID]
	gs.mutex.RUnlock()
	
	if !roomExists {
		return
	}
	
	room.mutex.RLock()
	defer room.mutex.RUnlock()
	
	for id, player := range room.Players {
		if id != excludePlayerID && player.IsConnected && player.Conn != nil {
			err := player.Conn.WriteJSON(message)
			if err != nil {
				log.Printf("Error sending message to player %s in room %s: %v", id, roomID, err)
				// Mark player as disconnected
				go gs.DisconnectPlayer(id, roomID)
			}
		}
	}
}

// GetAllPlayersInRoom returns all connected players in a room except the requesting one
func (gs *GameServer) GetAllPlayersInRoom(roomID string, excludePlayerID string) []*Player {
	gs.mutex.RLock()
	room, roomExists := gs.rooms[roomID]
	gs.mutex.RUnlock()
	
	if !roomExists {
		return []*Player{}
	}
	
	room.mutex.RLock()
	defer room.mutex.RUnlock()
	
	var players []*Player
	for id, player := range room.Players {
		if id != excludePlayerID && player.IsConnected {
			// Create a copy without the connection
			playerCopy := &Player{
				ID:       player.ID,
				Name:     player.Name,
				Position: player.Position,
				Rotation: player.Rotation,
				Color:    player.Color,
				RoomID:   player.RoomID,
			}
			players = append(players, playerCopy)
		}
	}
	return players
}

// generatePlayerColor returns a unique color for each player
func generatePlayerColor(playerID string) string {
	colors := []string{
		"#ff4444", "#44ff44", "#4444ff", "#ffff44", 
		"#ff44ff", "#44ffff", "#ff8844", "#88ff44",
		"#8844ff", "#ff4488", "#44ff88", "#4488ff",
	}
	
	// Simple hash to pick a color based on player ID
	hash := 0
	for _, char := range playerID {
		hash += int(char)
	}
	return colors[hash%len(colors)]
}

func (gs *GameServer) handleConnection(w http.ResponseWriter, r *http.Request) {
	// Check connection capacity
	if !connectionMonitor.CheckCapacity() {
		stats := connectionMonitor.GetStats()
		log.Printf("🚨 Connection limit reached (%d%%), rejecting new connection", stats["usage"])
		http.Error(w, "Server overloaded", http.StatusTooManyRequests)
		return
	}
	
	// Extract room ID from query parameters
	roomID := r.URL.Query().Get("room")
	if roomID == "" {
		roomID = "default"
	}
	
	log.Printf("🔗 New connection request from %s to room '%s' (Origin: %s)", r.RemoteAddr, roomID, r.Header.Get("Origin"))
	
	// Add WebSocket-specific CORS headers before upgrade
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("❌ Failed to upgrade connection from %s: %v", r.RemoteAddr, err)
		return
	}
	defer conn.Close()

	log.Printf("✅ WebSocket upgrade successful for %s in room %s", r.RemoteAddr, roomID)

	// Generate unique player ID and create player immediately
	playerID := generatePlayerID()
	log.Printf("🆔 Generated player ID: %s for room %s", playerID, roomID)
	
	currentPlayer := &Player{
		ID:   playerID,
		Name: "Player " + playerID[7:13], // Use part of the unique ID
		Position: map[string]float64{
			"x": 0.0,
			"y": 1.0,
			"z": 0.0,
		},
		Rotation: map[string]float64{
			"x": 0.0,
			"y": 0.0,
			"z": 0.0,
		},
		Color:  generatePlayerColor(playerID),
		RoomID: roomID,
		Conn:   conn,
	}

	log.Printf("👤 Created player: %s (ID: %s, Color: %s) for room %s", currentPlayer.Name, playerID, currentPlayer.Color, roomID)

	// Add player to room
	gs.AddPlayer(currentPlayer)
	log.Printf("➕ Player %s added to room %s", currentPlayer.Name, roomID)
	
	// Get room statistics for debugging
	room := gs.GetOrCreateRoom(roomID)
	room.mutex.RLock()
	playerCount := 0
	connectedCount := 0
	for _, p := range room.Players {
		playerCount++
		if p.IsConnected {
			connectedCount++
		}
	}
	room.mutex.RUnlock()
	
	log.Printf("📊 Room %s stats: %d total players, %d connected", roomID, playerCount, connectedCount)
	
	// Get all other players for init message
	allPlayers := gs.GetAllPlayersInRoom(roomID, playerID)
	log.Printf("👥 Found %d other players in room %s for player %s", len(allPlayers), roomID, currentPlayer.Name)

	// Send initial data to the new player
	initMsg := Message{
		Type:     "init",
		PlayerID: playerID,
		Data: map[string]interface{}{
			"yourId":      playerID,
			"yourColor":   currentPlayer.Color,
			"yourName":    currentPlayer.Name,
			"roomId":      roomID,
			"allPlayers":  allPlayers,
		},
	}
	
	log.Printf("📤 Sending init message to Player %s in room %s with %d other players...", currentPlayer.Name, roomID, len(allPlayers))
	err = conn.WriteJSON(initMsg)
	if err != nil {
		log.Printf("❌ Error sending init message to player %s: %v", playerID, err)
		gs.DisconnectPlayer(playerID, roomID)
		return
	}
	log.Printf("✅ Init message sent successfully to Player %s in room %s", currentPlayer.Name, roomID)

	// Notify other players in the room about the new player
	newPlayerMsg := Message{
		Type:     "newPlayer",
		PlayerID: playerID,
		Data: map[string]interface{}{
			"id":       playerID,
			"name":     currentPlayer.Name,
			"color":    currentPlayer.Color,
			"position": currentPlayer.Position,
			"rotation": currentPlayer.Rotation,
		},
	}
	
	log.Printf("📢 Broadcasting new player %s to %d other players in room %s", currentPlayer.Name, len(allPlayers), roomID)
	gs.BroadcastToRoom(newPlayerMsg, roomID, playerID)
	log.Printf("✅ Notified other players in room %s about Player %s", roomID, currentPlayer.Name)

	// Handle messages from this connection
	for {
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Printf("⚠️ Read error from player %s in room %s: %v", currentPlayer.Name, roomID, err)
			break
		}

		// Process the message based on type
		switch msg.Type {
		case "reconnect":
			// Handle reconnection attempt - try to merge with existing player
			if previousPlayerID, ok := msg.Data["previousPlayerId"].(string); ok {
				log.Printf("🔄 Received reconnect request for previous player: %s in room %s", previousPlayerID, roomID)
				
				// Try to find and merge with previous player
				room := gs.GetOrCreateRoom(roomID)
				room.mutex.Lock()
				
				if existingPlayer, exists := room.Players[previousPlayerID]; exists && !existingPlayer.IsConnected {
					// Merge current player with existing player state
					log.Printf("🔄 Merging with existing player %s, preserving position", previousPlayerID)
					
					// Close current player connection and remove
					delete(room.Players, currentPlayer.ID)
					
					// Update existing player with new connection
					existingPlayer.Conn = conn
					existingPlayer.IsConnected = true
					existingPlayer.LastSeen = time.Now()
					currentPlayer = existingPlayer
					
					room.mutex.Unlock()
					
					// Send reconnection success message with preserved state
					reconnectMsg := Message{
						Type:     "reconnected",
						PlayerID: existingPlayer.ID,
						Data: map[string]interface{}{
							"yourId":      existingPlayer.ID,
							"yourColor":   existingPlayer.Color,
							"yourName":    existingPlayer.Name,
							"roomId":      roomID,
							"allPlayers":  gs.GetAllPlayersInRoom(roomID, existingPlayer.ID),
							"position":    existingPlayer.Position,
							"rotation":    existingPlayer.Rotation,
						},
					}
					
					err = conn.WriteJSON(reconnectMsg)
					if err != nil {
						log.Printf("❌ Error sending reconnect message: %v", err)
						gs.DisconnectPlayer(existingPlayer.ID, roomID)
						break
					}
					
					log.Printf("✅ Player %s successfully reconnected to room %s with preserved state", existingPlayer.Name, roomID)
					
					// Notify other players about reconnection
					newPlayerMsg := Message{
						Type:     "newPlayer",
						PlayerID: existingPlayer.ID,
						Data: map[string]interface{}{
							"id":       existingPlayer.ID,
							"name":     existingPlayer.Name,
							"color":    existingPlayer.Color,
							"position": existingPlayer.Position,
							"rotation": existingPlayer.Rotation,
						},
					}
					gs.BroadcastToRoom(newPlayerMsg, roomID, existingPlayer.ID)
				} else {
					room.mutex.Unlock()
					log.Printf("⚠️ Previous player %s not found or already connected in room %s, continuing with new player", previousPlayerID, roomID)
				}
			}
			
		case "updatePosition":
			if data, ok := msg.Data["position"].(map[string]interface{}); ok {
				position := make(map[string]float64)
				for k, v := range data {
					if val, ok := v.(float64); ok {
						position[k] = val
					}
				}
				
				var rotation map[string]float64
				if rotData, ok := msg.Data["rotation"].(map[string]interface{}); ok {
					rotation = make(map[string]float64)
					for k, v := range rotData {
						if val, ok := v.(float64); ok {
							rotation[k] = val
						}
					}
				}
				
				gs.UpdatePlayer(currentPlayer.ID, roomID, position, rotation)
				
				// Broadcast position update to other players in the room
				updateMsg := Message{
					Type:     "playerUpdate",
					PlayerID: currentPlayer.ID,
					Data: map[string]interface{}{
						"position": position,
						"rotation": rotation,
					},
				}
				gs.BroadcastToRoom(updateMsg, roomID, currentPlayer.ID)
			}
		default:
			log.Printf("🤷 Unknown message type from player %s in room %s: %s", currentPlayer.Name, roomID, msg.Type)
		}
	}

	// Player disconnected
	log.Printf("🚪 Player %s disconnecting from room %s", currentPlayer.Name, roomID)
	if currentPlayer != nil {
		gs.DisconnectPlayer(currentPlayer.ID, roomID)
	}
}

func generatePlayerID() string {
	// Generate a more unique ID using crypto/rand and timestamp
	timestamp := time.Now().UnixNano()
	randomBytes := make([]byte, 4)
	rand.Read(randomBytes)
	return fmt.Sprintf("player-%x-%x", timestamp&0xFFFFFF, randomBytes)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers for all requests
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization, X-Requested-With")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		
		// Handle preflight OPTIONS requests
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		
		// Continue to the next handler
		next.ServeHTTP(w, r)
	})
}

func main() {
	gameServer := NewGameServer()

	// Create a new ServeMux for better routing
	mux := http.NewServeMux()
	
	// Add routes
	mux.HandleFunc("/ws", gameServer.handleConnection)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		
		stats := connectionMonitor.GetStats()
		response := fmt.Sprintf(`{
			"status": "Flying Car Game Server Running - Enhanced Connection Management",
			"features": [
				"Room-based multiplayer",
				"Player state persistence (5min timeout)",
				"WebSocket real-time communication",
				"Advanced connection pool management",
				"DoS protection & monitoring",
				"Emergency cleanup procedures"
			],
			"connectionStats": {
				"active": %d,
				"max": %d,
				"usage": %d
			}
		}`, stats["active"], stats["max"], stats["usage"])
		
		w.Write([]byte(response))
	})
	
	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		
		stats := connectionMonitor.GetStats()
		status := "healthy"
		if stats["usage"].(int) > 90 {
			status = "stressed"
		}
		
		response := fmt.Sprintf(`{
			"status": "%s",
			"uptime": %d,
			"connections": {
				"active": %d,
				"max": %d,
				"usage": %d
			},
			"timestamp": "%s"
		}`, status, int(time.Now().Unix()), stats["active"], stats["max"], stats["usage"], time.Now().Format(time.RFC3339))
		
		w.Write([]byte(response))
	})

	// Wrap the mux with CORS middleware
	handler := corsMiddleware(mux)

	// Get port from environment variable (Railway/cloud deployment)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8082" // Default port for local development
	}
	
	// Bind to 0.0.0.0 for cloud deployment
	host := "0.0.0.0"
	address := fmt.Sprintf("%s:%s", host, port)

	log.Println("WebSocket server starting on", address)
	log.Printf("🌐 WebSocket endpoint: ws://%s/ws?room=ROOM_ID", address)
	log.Printf("🏥 Health check: http://%s/health", address)
	log.Printf("🏠 Room-based multiplayer enabled")
	log.Printf("💾 Player state persistence enabled (5min timeout)")
	log.Printf("🔒 Connection pool management enabled (max: %d)", connectionMonitor.maxConnections)
	log.Printf("⚡ Ready for Railway deployment")
	log.Fatal(http.ListenAndServe(address, handler))
} 