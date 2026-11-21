package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	localws "github.com/gorilla/websocket"
	"github.com/justtrackio/gosoline/pkg/apiserver"
	"github.com/justtrackio/gosoline/pkg/appctx"
	"github.com/justtrackio/gosoline/pkg/cfg"
	"github.com/justtrackio/gosoline/pkg/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Start the production router, including its authentication, compression middleware,
// legacy bridge and upstream REST handlers. Existing bridge tests exercise the
// bridge in isolation; these tests catch mistakes when merging both sets of routes.
func startProductionAPI(t *testing.T) string {
	t.Helper()

	config := cfg.NewWithInterfaces(cfg.NewMemoryEnvProvider())
	require.NoError(t, config.Option(cfg.WithConfigMap(map[string]interface{}{
		"api":                             map[string]interface{}{"port": "0", "mode": "test"},
		"api_auth_keys":                   []string{testApiKey},
		"api_cors_allowed_origin_pattern": "",
		"api_cors_allowed_headers":        []string{"X-API-KEY", "Content-Type"},
		"api_cors_allowed_methods":        []string{"GET", "POST"},
	})))

	ctx, cancel := context.WithCancel(appctx.WithContainer(context.Background()))
	module, err := apiserver.New(DefineRouter)(ctx, config, log.NewLogger())
	if err != nil {
		cancel()
		require.NoError(t, err)
	}
	server := module.(*apiserver.ApiServer)
	port, err := server.GetPort()
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("production API did not shut down")
		}
	})

	return fmt.Sprintf("http://127.0.0.1:%d", *port)
}

func postWebsocketJSON(t *testing.T, apiURL, path string, input map[string]interface{}) map[string]interface{} {
	t.Helper()

	payload, err := json.Marshal(input)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, apiURL+path, bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", testApiKey)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var output map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&output))
	return output
}

func TestWebsocketUpgrade_ProductionRoutesRequireAuthentication(t *testing.T) {
	apiURL := startProductionAPI(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/ws"},
		{http.MethodPost, "/api/ws/connect"},
		{http.MethodPost, "/api/ws/read"},
		{http.MethodPost, "/api/ws/write"},
		{http.MethodPost, "/api/ws/close"},
		{http.MethodPost, "/api/ws/stats"},
	} {
		t.Run(route.path, func(t *testing.T) {
			req, err := http.NewRequest(route.method, apiURL+route.path, bytes.NewBufferString("{}"))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		})
	}
}

func TestWebsocketUpgrade_RestAndBridgeCoexist(t *testing.T) {
	apiURL := startProductionAPI(t)
	remote := startRemoteEchoServer(t, nil)

	headers := http.Header{}
	headers.Set("X-API-KEY", testApiKey)
	headers.Set(wsConfigHeaderName, wsConfigHeaderValue(t, echoConfig(remote)))
	dialer := localws.Dialer{HandshakeTimeout: 5 * time.Second}
	bridge, resp, err := dialer.Dial(toWsUrl(apiURL)+"/api/ws", headers)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	defer bridge.Close()
	require.NoError(t, bridge.SetReadDeadline(time.Now().Add(10*time.Second)))
	require.NoError(t, bridge.SetWriteDeadline(time.Now().Add(10*time.Second)))

	connected := postWebsocketJSON(t, apiURL, "/api/ws/connect", map[string]interface{}{
		"url":                          toWsUrl(remote.URL),
		"tlsClientIdentifier":          "chrome_133",
		"insecureSkipVerify":           true,
		"handshakeTimeoutMilliseconds": 3000,
	})
	require.EqualValues(t, http.StatusSwitchingProtocols, connected["status"])
	connectionID, ok := connected["connectionId"].(string)
	require.True(t, ok)
	require.NotEmpty(t, connectionID)
	t.Cleanup(func() {
		postWebsocketJSON(t, apiURL, "/api/ws/close", map[string]interface{}{"connectionId": connectionID})
	})

	timedOut := postWebsocketJSON(t, apiURL, "/api/ws/read", map[string]interface{}{
		"connectionId": connectionID, "timeoutMilliseconds": 20,
	})
	require.EqualValues(t, 0, timedOut["status"])
	require.Contains(t, timedOut["body"], "websocket read timeout")

	for _, message := range []struct {
		messageType int
		data        string
	}{
		{localws.TextMessage, "42[\"event\",{\"x\":1}]"},
		{localws.BinaryMessage, base64.StdEncoding.EncodeToString([]byte{0x00, 0x01, 0x02, 0xff})},
	} {
		written := postWebsocketJSON(t, apiURL, "/api/ws/write", map[string]interface{}{
			"connectionId": connectionID, "messageType": message.messageType,
			"data": message.data, "timeoutMilliseconds": 3000,
		})
		require.Equal(t, true, written["success"])
		read := postWebsocketJSON(t, apiURL, "/api/ws/read", map[string]interface{}{
			"connectionId": connectionID, "timeoutMilliseconds": 3000,
		})
		require.EqualValues(t, message.messageType, read["messageType"])
		require.Equal(t, message.data, read["data"])
	}

	stats := postWebsocketJSON(t, apiURL, "/api/ws/stats", map[string]interface{}{"connectionId": connectionID})
	require.Equal(t, connectionID, stats["connectionId"])
	require.EqualValues(t, 2, stats["messagesRead"])
	require.EqualValues(t, 2, stats["messagesWritten"])
	closed := postWebsocketJSON(t, apiURL, "/api/ws/close", map[string]interface{}{"connectionId": connectionID})
	require.Equal(t, true, closed["success"])

	// Freeing the upstream REST session store must not terminate a bridge-owned client.
	req, err := http.NewRequest(http.MethodGet, apiURL+"/api/free-all", nil)
	require.NoError(t, err)
	req.Header.Set("X-API-KEY", testApiKey)
	client := &http.Client{Timeout: 5 * time.Second}
	freed, err := client.Do(req)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, freed.Body)
	freed.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, freed.StatusCode)

	payload := []byte{0x00, 0xff, 0x42}
	require.NoError(t, bridge.WriteMessage(localws.BinaryMessage, payload))
	messageType, echoed, err := bridge.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, localws.BinaryMessage, messageType)
	assert.Equal(t, payload, echoed)
}
