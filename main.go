package main

import (
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"log"
	"net/http"
	"strings"
	"time"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Client struct {
	conn     *websocket.Conn
	userId   string
	username string
	roomId   string
}

// 添加心跳检测机制
func (c *Client) keepAlive() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

type Room struct {
	Id       string
	Password string
	Clients  map[*Client]bool
	Created  time.Time
}

var (
	rooms   = make(map[string]*Room)
	clients = make(map[*Client]bool)
)

func main() {
	http.Handle("/", http.FileServer(http.Dir("./static")))
	http.HandleFunc("/ws", handleWebSocket)

	log.Println("Server started on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}
	defer conn.Close()

	client := &Client{
		conn:   conn,
		userId: uuid.New().String()[:8],
	}

	go client.keepAlive()

	// 发送 userId 到客户端
	sendUserID(client, client.userId)

	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}

		switch msg["type"] {
		case "create_room":
			handleCreateRoom(client, msg)
		case "join_room":
			handleJoinRoom(client, msg)
		case "message":
			handleRoomMessage(client, msg)
		case "leave_room":
			handleLeaveRoom(client)
		}
	}

	handleLeaveRoom(client)
}

// 新增发送 userId 的函数
func sendUserID(client *Client, userId string) {
	client.conn.WriteJSON(map[string]interface{}{
		"type":   "user_id",
		"userId": userId,
	})
}

func handleCreateRoom(client *Client, msg map[string]interface{}) {
	roomId := generateRoomID()
	password, _ := msg["password"].(string)
	username, _ := msg["username"].(string)

	if username != "匿名用户" {
		// 检查用户名是否重复
		for c := range clients {
			if c.username == username {
				sendError(client, "用户名已存在")
				return
			}
		}

	}

	room := &Room{
		Id:       roomId,
		Password: password,
		Clients:  make(map[*Client]bool),
		Created:  time.Now(),
	}

	rooms[roomId] = room

	client.roomId = roomId
	client.username, _ = msg["username"].(string)
	room.Clients[client] = true
	clients[client] = true

	sendShowChatPanel(client)
	sendSystemMessage(client, "房间创建成功，ID: "+roomId)
	broadcastUsers(room.Id)

	// 添加房间信息广播
	broadcastToRoom(roomId, map[string]interface{}{
		"type":    "room_info",
		"room_id": roomId,
	})

	broadcastToRoom(roomId, map[string]interface{}{
		"type":    "system",
		"content": client.username + " 进入了房间",
	})
}

func handleJoinRoom(client *Client, msg map[string]interface{}) {
	roomId, _ := msg["room_id"].(string)
	username, _ := msg["username"].(string)

	if username != "匿名用户" {
		// 检查用户名是否重复
		for c := range clients {
			if c.username == username {
				sendError(client, "用户名已存在")
				return
			}
		}
	}

	// 先进行存在性检查（只读锁）
	room, exists := rooms[msg["room_id"].(string)]

	if !exists {
		sendError(client, "房间不存在")
		return
	}

	// 单独处理密码校验
	if room.Password != "" && room.Password != msg["password"].(string) {
		sendError(client, "密码错误")
		return
	}

	sendShowRoomPanel(client)

	client.roomId = msg["room_id"].(string)
	client.username = msg["username"].(string)
	room.Clients[client] = true
	clients[client] = true

	sendSystemMessage(client, "成功加入房间 "+roomId)
	broadcastToRoom(roomId, map[string]interface{}{
		"type":    "system",
		"content": username + " 加入了房间",
	})

	// 广播当前房间用户列表
	broadcastUsers(roomId)

	// 发送 userId 到客户端
	sendUserID(client, client.userId)
}

func handleRoomMessage(client *Client, msg map[string]interface{}) {
	if room, exists := rooms[client.roomId]; exists {
		message := map[string]interface{}{
			"type":      "message",
			"from":      msg["from"], // 确保接收者能够获取发送者信息
			"content":   msg["content"],
			"timestamp": msg["timestamp"], // 确保接收者能够获取时间戳
			"userId":    client.userId,    // 添加发送者的 userId
			"room_id":   client.roomId,
		}
		broadcastToRoom(room.Id, message)
		broadcastUsers(room.Id)
	}
}

func handleLeaveRoom(client *Client) {
	if room, exists := rooms[client.roomId]; exists {
		delete(room.Clients, client)
		broadcastToRoom(room.Id, map[string]interface{}{
			"type":    "system",
			"content": client.username + " 离开了房间",
		})

		if len(room.Clients) == 0 {
			delete(rooms, room.Id)
		}
		broadcastUsers(room.Id)
	}
	delete(clients, client)
}

func generateRoomID() string {
	return uuid.New().String()[:4]
}

func broadcastToRoom(roomId string, message interface{}) {
	if room, exists := rooms[roomId]; exists {
		for client := range room.Clients {
			if err := client.conn.WriteJSON(message); err != nil {
				log.Println("Write error:", err)
				client.conn.Close()
				delete(room.Clients, client)
			}
		}
	}
}

func broadcastUsers(roomId string) {
	if room, exists := rooms[roomId]; exists {
		users := []string{}
		for client := range room.Clients {
			users = append(users, client.username)
		}
		broadcastToRoom(roomId, map[string]interface{}{
			"type":    "system",
			"content": "当前房间用户: " + strings.Join(users, ", "),
		})
	}
}

func sendShowChatPanel(client *Client) {
	client.conn.WriteJSON(map[string]interface{}{
		"type": "show_chat_panel",
	})
}

func sendShowRoomPanel(client *Client) {
	client.conn.WriteJSON(map[string]interface{}{
		"type": "show_room_panel",
	})
}

func sendSystemMessage(client *Client, content string) {
	client.conn.WriteJSON(map[string]interface{}{
		"type":    "system",
		"content": content,
	})
}

func sendError(client *Client, error string) {
	client.conn.WriteJSON(map[string]interface{}{
		"type":  "error",
		"error": error,
	})
}
