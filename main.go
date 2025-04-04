package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Constants for configuration and magic strings
const (
	WSRoute          = "/ws"
	ServerPort       = ":8080"
	MaxUsernameLen   = 20
	UsernameTimeout  = 15 * time.Second
	UsernameAttempts = 3
	TypeTextTyping   = "/typing"
	TypeTextStop     = "/stoptyping"
	TypeTextPM       = "/pm"
	TypeBinaryImage  = "image"
)

var (
	usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

type Client struct {
	conn     *websocket.Conn
	username string
}

type ChatServer struct {
	clients   map[*Client]bool
	usernames map[string]bool
	mutex     sync.RWMutex
	upgrader  websocket.Upgrader
}

func NewChatServer() *ChatServer {
	return &ChatServer{
		clients:   make(map[*Client]bool),
		usernames: make(map[string]bool),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true }, // TODO: Restrict in prod
		},
	}
}

func (cs *ChatServer) HandleConnection(w http.ResponseWriter, r *http.Request) {
	log.Printf("New connection attempt")
	conn, err := cs.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

		username, err := cs.authenticateUser(conn)
	if err != nil {
		log.Printf("Authentication failed: %v", err)
		return
	}
	//conn.SetReadDeadline(time.Time{}) // Reset deadline

	client := &Client{conn: conn, username: username}
	client.conn.WriteMessage(websocket.TextMessage,[]byte(username))
	cs.addClient(client)

	log.Printf("%s connected (Total: %d)", username, cs.clientCount())
	cs.notifyUserEvent(client, "joined")

	// Main message loop
	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("%s disconnected: %v", username, err)
			cs.removeClient(client) // Cleanup here, no defer needed for notifications
			return
		}

		switch msgType {
		case websocket.TextMessage:
			cs.handleTextMessage(client, string(msg))
		case websocket.BinaryMessage:
			cs.handleBinaryMessage(client, msg, TypeBinaryImage)
		}
	}
}

func (cs *ChatServer) authenticateUser(conn *websocket.Conn) (string, error) {

	
	readDeadline := time.Now().Add(UsernameTimeout) // Deadline time-moment
	conn.SetReadDeadline(time.Now().Add(UsernameTimeout))
	defer conn.SetReadDeadline(time.Time{}) //reset deadline
	for attempts := 0; attempts < UsernameAttempts; attempts++ {
		_, usernameBytes, err := conn.ReadMessage()
		if err != nil {
			if time.Now().After(readDeadline){
				break
			}
			return "", fmt.Errorf("failed to read username: %v", err)
		}
		username := strings.ToLower(strings.TrimSpace(string(usernameBytes)))
		if len(username) == 0 || len(username) > MaxUsernameLen {
			conn.WriteMessage(websocket.TextMessage,[]byte("Username must be 1-20 characters"))
			continue
		}
		if !usernameRegex.MatchString(username) {
			conn.WriteMessage(websocket.TextMessage,[]byte("Invalid username (user a-z, 0-9, _, -)"))
			continue
		}

		cs.mutex.Lock()
		if cs.usernames[username] {
			conn.WriteMessage(websocket.TextMessage,[]byte("Username taken, try again"))
			cs.mutex.Unlock()
			continue
		}
		cs.usernames[username] = true
		cs.mutex.Unlock()
		return username, nil}

		return cs.assignGuestUsername(conn)
	}
	

	func (cs *ChatServer) assignGuestUsername(conn *websocket.Conn) (string, error) {
		cs.mutex.Lock()
		defer cs.mutex.Unlock()

	 for i:= 0; i < 10000; i++ {
		username := fmt.Sprintf("guest%d", 1000 + rand.Intn(9000))
		if !cs.usernames[username] {
			cs.usernames[username] = true
			conn.WriteMessage(websocket.TextMessage, 
			[]byte (fmt.Sprintf("Assigned username: %s", username)))
			return username, nil
		}
	 }
	 return "", fmt.Errorf("could not assign guest username")

	}


func (cs *ChatServer) addClient(client *Client) {
	cs.mutex.Lock()
	cs.clients[client] = true
	cs.mutex.Unlock()

	cs.sendUserList(client)
}

func (cs *ChatServer) removeClient(client *Client) {
	cs.mutex.Lock()
	_, exists := cs.clients[client]
	if !exists {
		cs.mutex.Unlock()
		return
	}
	delete(cs.clients, client)
	delete(cs.usernames, client.username)
	cs.mutex.Unlock()

	// Notify outside lock
	log.Printf("Removing %s", client.username)
	cs.notifyUserEvent(client, "left")
}

func (cs *ChatServer) notifyUserEvent(client *Client, action string) {
	cs.Broadcast([]byte(fmt.Sprintf("%s %s", client.username, action)), websocket.TextMessage, client)
	var signal string
	if action == "joined" {
		signal = "+"
	} else if action == "left" {
		signal = "-"
	}
	cs.Broadcast([]byte(fmt.Sprintf("%s%s", signal, client.username)), websocket.TextMessage, client) // + or -
}

func (cs *ChatServer) handleTextMessage(client *Client, msg string) {
	switch {
	case msg == TypeTextTyping:
		cs.Broadcast([]byte(fmt.Sprintf("%s is typing...", client.username)), websocket.TextMessage, client)
	case msg == TypeTextStop:
		cs.Broadcast([]byte(fmt.Sprintf("%s stopped typing", client.username)), websocket.TextMessage, client)
	case strings.HasPrefix(msg, TypeTextPM):
		cs.handlePrivateMessage(client, msg)
	default:
		cs.Broadcast([]byte(fmt.Sprintf("%s: %s", client.username, msg)), websocket.TextMessage, client)
	}
}

func (cs *ChatServer) handleBinaryMessage(client *Client, data []byte, msgType string) {
	prefix := []byte(fmt.Sprintf("%s sent %s:|", client.username, msgType))
	cs.Broadcast(append(prefix, data...), websocket.BinaryMessage, client)
}

func (cs *ChatServer) handlePrivateMessage(sender *Client, msg string) {
	parts := strings.SplitN(msg[4:], " ", 2)
	if len(parts) < 2 {
		sender.conn.WriteMessage(websocket.TextMessage, []byte("Usage: /pm <user> <message>"))
		return
	}

	target, message := parts[0], parts[1]
	if err := cs.sendPrivateMessage(sender, target, message); err != nil {
		sender.conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Failed to send PM to %s", target)))
	}
}

func (cs *ChatServer) Broadcast(msg []byte, msgType int, sender *Client) {
	cs.mutex.RLock()
	//log.Printf("Broadcasting with RLock: %s", msg)
	defer cs.mutex.RUnlock()

	for client := range cs.clients {
		if client == sender {
			continue
		}
		if err := client.conn.WriteMessage(msgType, msg); err != nil {
			log.Printf("Broadcast to %s failed: %v", client.username, err)
			client.conn.Close() // Close bad connection, let ReadMessage handle removal
		}
	}
}

func (cs *ChatServer) sendPrivateMessage(sender *Client, targetUsername, msg string) error {
	cs.mutex.RLock()
	target := cs.findClientByUsername(targetUsername)
	cs.mutex.RUnlock()
	if target == nil {
		return fmt.Errorf("user %s not found", targetUsername)
	}

	if sender.username == targetUsername {
		return fmt.Errorf("can't message yourself")
	}

	pm := fmt.Sprintf("[PM from %s]: %s", sender.username, msg)
	if err := target.conn.WriteMessage(websocket.TextMessage, []byte(pm)); err != nil {
		return fmt.Errorf("delivery failed: %v", err)
	}
	sender.conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("[PM to %s]: %s", targetUsername, msg)))
	return nil
}

func (cs *ChatServer) findClientByUsername(username string) *Client {
	for client := range cs.clients {
		if client.username == username {
			return client
		}
	}
	return nil
}

func (cs *ChatServer) sendUserList(client *Client) {
	cs.mutex.RLock()
	users := make([]string, 0, len(cs.usernames))
	for u := range cs.usernames {
		users = append(users, u)
	}
	cs.mutex.RUnlock()

	msg := []byte("/users " + strings.Join(users, ","))
	if err := client.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		log.Printf("Failed to send user list to %s: %v", client.username, err)
	}
}

func (cs *ChatServer) clientCount() int {
	cs.mutex.RLock()
	defer cs.mutex.RUnlock()
	return len(cs.clients)
}

func main() {
	server := NewChatServer()
	http.HandleFunc(WSRoute, server.HandleConnection)
	log.Printf("ChatSphere server started on http://localhost%s", ServerPort)
	if err := http.ListenAndServe(ServerPort, nil); err != nil {
		log.Fatal(err)
	}
}