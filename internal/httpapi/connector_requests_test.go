package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goldenfinger/agent/internal/collection"
	"goldenfinger/agent/internal/store"
)

func TestConnectorRequestEndpointsEnforceOwner(t *testing.T) {
	srv, _ := newWebTestServer(t)
	manager := collection.New(srv.Repos, srv.Settings)
	srv.Collection = manager
	owner := &store.User{UserType: store.UserGeneral, Name: "机主", TZ: "Asia/Shanghai"}
	if err := srv.Repos.Users.Create(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	ownerID := owner.ID
	other := &store.User{UserType: store.UserGeneral, Name: "其他用户", TZ: "Asia/Shanghai"}
	if err := srv.Repos.Users.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}

	type answer struct {
		result collection.ConnectorResult
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		result, err := manager.Request(context.Background(), collection.ConnectorRequest{
			OwnerUserID: ownerID, Channel: store.ChannelActivity, Kind: "set_activity", Enabled: true,
		})
		done <- answer{result, err}
	}()

	h := srv.Handler()
	var pending map[string]any
	deadline := 100
	for deadline > 0 {
		recorder := getJSON(t, h, "/api/channels/connector/requests?channel=activity")
		pending = decodeMap(t, recorder)
		if len(pending["requests"].([]any)) == 1 {
			break
		}
		deadline--
	}
	requests := pending["requests"].([]any)
	if len(requests) != 1 {
		t.Fatalf("pending=%v", pending)
	}
	request := requests[0].(map[string]any)
	id := request["id"].(string)

	wrong := httptest.NewRequest(http.MethodPost, "/api/channels/connector/requests/"+id+"/complete", strings.NewReader(`{"ok":true,"enabled":true}`))
	wrong.Header.Set("X-User-Id", other.ID)
	wrongRecorder := httptest.NewRecorder()
	h.ServeHTTP(wrongRecorder, wrong)
	if wrongRecorder.Code != http.StatusNotFound {
		t.Fatalf("wrong owner=%d %s", wrongRecorder.Code, wrongRecorder.Body)
	}

	right := postJSON(t, h, "/api/channels/connector/requests/"+id+"/complete", `{"ok":true,"enabled":true}`)
	if right.Code != http.StatusOK {
		t.Fatalf("complete=%d %s", right.Code, right.Body)
	}
	result := <-done
	if result.err != nil || !result.result.OK || result.result.Enabled == nil || !*result.result.Enabled {
		t.Fatalf("result=%+v err=%v", result.result, result.err)
	}
	unknown := postJSON(t, h, "/api/channels/connector/requests/unknown/complete", `{"ok":true}`)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown=%d %s", unknown.Code, unknown.Body)
	}
}
