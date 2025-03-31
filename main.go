package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

// Client holds a connection and its username
type Client struct {
  conn *websocket.Conn
  username string
}

// ChatServer manages connected clients and broadcasting
type ChatServer struct {
    clients    map[*Client]bool
    usernames  map[string]bool
    mutex      sync.Mutex // For safe concurrent access to clients
    upgrader   websocket.Upgrader
}

// NewChatServer initializes a new ChatServer
func NewChatServer() *ChatServer {
    return &ChatServer{
        clients: make(map[*Client]bool),
        usernames: make(map[string]bool),
        upgrader: websocket.Upgrader{
            CheckOrigin: func(r *http.Request) bool { return true }, // Avoiding CORS for testing purposes...
        },
    }
}

// HandleConnection manages a single client connection
func (cs *ChatServer) HandleConnection(w http.ResponseWriter, r *http.Request) {
    conn, err := cs.upgrader.Upgrade(w, r, nil) //  Handshake
    if err != nil {
        log.Println("Upgrade failed:", err)
        return
    }
    defer conn.Close()

    // Get username from client
    _, usernameBytes, err := conn.ReadMessage()
    if err != nil {
      log.Println("Failed to get username:", err)
      return 
    }
    username := string(usernameBytes)

    // Register client
    cs.mutex.Lock()
    if cs.usernames[username] {
      conn.WriteMessage(websocket.TextMessage,[]byte("Username taken, try again"))
      cs.mutex.Unlock()
      return
    }
    cs.usernames[username]=true
    cs.mutex.Unlock()
    
    client := &Client{conn:conn,username: username}

    cs.mutex.Lock()
    cs.clients[client] = true
    clientCount := len(cs.clients)
    cs.mutex.Unlock()

    log.Printf("%s connected! Total: %d",username, clientCount)
    cs.Broadcast([]byte(fmt.Sprintf("%s joined the chat", username)), websocket.TextMessage, nil)

    // Handle messages
    for {
        msgType, msg, err := conn.ReadMessage()
        if err != nil {
            log.Println(username,"disconnected:", err)
            cs.removeClient(client)
            cs.Broadcast([]byte(fmt.Sprintf("%s left the chat",username)), websocket.TextMessage,client)
            return
        }
        msgStr := string(msg)
        log.Printf("Received from %s: %s", username, msgStr)
          
        if msgStr == "/typing" {
            cs.Broadcast([]byte(fmt.Sprintf("%s is typing...", username)),websocket.TextMessage,client)
        }else if msgStr == "/stoptyping" {
          cs.Broadcast([]byte(fmt.Sprintf("%s stopped typing",username)), websocket.TextMessage,client)
        }else if strings.HasPrefix(msgStr,"/pm") {
          parts := strings.SplitN(msgStr[4:]," ",2) // Skip "/pm"
          if len(parts) < 2 {
            client.conn.WriteMessage(websocket.TextMessage,[]byte("Usage: /pm <username> <message>"))
            continue
          }
          targetUsername, pmMsg := parts[0], parts[1]
          cs.sendPrivateMessage(client, targetUsername, pmMsg)
        }else {
        fullMsg := []byte(fmt.Sprintf("%s: %s", username,msg))
        cs.Broadcast(fullMsg, msgType, client)}
    }
}

// Broadcast sends a message to all clients except the sender (if provided)
func (cs *ChatServer) Broadcast(msg []byte, msgType int, sender *Client) {
    cs.mutex.Lock()
    defer cs.mutex.Unlock()

    for client := range cs.clients {
        if client == sender { // Skip sender (optional feature)
            continue
        }
        if err := client.conn.WriteMessage(msgType,msg); err != nil {
          log.Println("Broadcast error:", err)
          client.conn.Close()
          delete(cs.clients,client)
        }
    }
}

func (cs *ChatServer) sendPrivateMessage(sender *Client, targetUsername, msg string){
  cs.mutex.Lock()
  defer cs.mutex.Unlock()
  target := cs.findClientByUsername(targetUsername)
  if target == nil {
    sender.conn.WriteMessage(websocket.TextMessage,[]byte(fmt.Sprintf("User %s not found",targetUsername)))
    return
  }
  pm := []byte(fmt.Sprintf("[PM from %s]: %s", sender.username,msg))
  if err := target.conn.WriteMessage(websocket.TextMessage, pm); err != nil {
    log.Println("Private message error:", err)
    target.conn.Close()
    delete(cs.clients,target)
  }
  sender.conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("[PM to %s]: %s",targetUsername,msg)))
}

func (cs *ChatServer) findClientByUsername(username string) *Client {
  for client := range cs.clients {
    if client.username == username {
      return client
    }
  }
  return nil
}
// removeClient safely removes a client from the map
func (cs *ChatServer) removeClient(client *Client) {
    cs.mutex.Lock()
    delete(cs.clients, client)
    delete(cs.usernames,client.username)
    cs.mutex.Unlock()
}

func main() {
    server := NewChatServer()
    http.HandleFunc("/ws", server.HandleConnection)
    log.Println("Chat server starting on :8080...")
    if err := http.ListenAndServe(":8080", nil); err != nil {
        log.Fatal("Server failed:", err)
    }
}