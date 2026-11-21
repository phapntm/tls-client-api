package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	localws "github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestWebsocketBridge_RemoteCompression(t *testing.T) {
	for _, testCase := range []struct {
		name              string
		acceptCompression bool
	}{
		{name: "accepted", acceptCompression: true},
		{name: "declined", acceptCompression: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			offeredExtensions := make(chan string, 1)
			upgrader := localws.Upgrader{
				EnableCompression: testCase.acceptCompression,
				CheckOrigin:       func(r *http.Request) bool { return true },
			}
			remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case offeredExtensions <- r.Header.Get("Sec-WebSocket-Extensions"):
				default:
				}
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				conn.EnableWriteCompression(testCase.acceptCompression)
				for {
					messageType, payload, err := conn.ReadMessage()
					if err != nil {
						return
					}
					if err := conn.WriteMessage(messageType, payload); err != nil {
						return
					}
				}
			}))
			t.Cleanup(remote.Close)
			bridge := startBridgeServer(t)

			conn, resp, err := dialBridge(t, bridge, testApiKey, echoConfig(remote))
			require.NoError(t, err)
			require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
			defer conn.Close()
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
			require.NoError(t, conn.SetWriteDeadline(time.Now().Add(5*time.Second)))

			select {
			case extensions := <-offeredExtensions:
				require.Contains(t, extensions, "permessage-deflate")
				require.Contains(t, extensions, "server_no_context_takeover")
				require.Contains(t, extensions, "client_no_context_takeover")
			case <-time.After(5 * time.Second):
				t.Fatal("remote server did not receive the compression offer")
			}

			// Exercise multiple message boundaries and both application message types.
			for index, message := range []struct {
				messageType int
				payload     []byte
			}{
				{localws.TextMessage, []byte(strings.Repeat("compressed websocket message ", 512))},
				{localws.BinaryMessage, bytes.Repeat([]byte{0x00, 0x01, 0x02, 0xff}, 2048)},
				{localws.TextMessage, []byte(`42["event",{"x":1}]`)},
				{localws.BinaryMessage, []byte{0x00, 0xff}},
				{localws.TextMessage, []byte{}},
			} {
				require.NoError(t, conn.WriteMessage(message.messageType, message.payload))
				messageType, payload, err := conn.ReadMessage()
				require.NoError(t, err)
				require.Equal(t, message.messageType, messageType, "message %d", index)
				require.Equal(t, message.payload, payload, "message %d", index)
			}
		})
	}
}
