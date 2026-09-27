package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedRecorder struct {
	rec *httptest.ResponseRecorder
	mu  sync.Mutex
}

func newLockedRecorder() *lockedRecorder {
	return &lockedRecorder{rec: httptest.NewRecorder()}
}

func (w *lockedRecorder) Header() http.Header {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rec.Header()
}

func (w *lockedRecorder) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rec.Write(b)
}

func (w *lockedRecorder) WriteHeader(statusCode int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rec.WriteHeader(statusCode)
}

func (w *lockedRecorder) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rec.Flush()
}

func (w *lockedRecorder) BodyString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rec.Body.String()
}

func TestEventBus_PubSub(t *testing.T) {
	eb := NewEventBus()

	if eb.HasSubscribers() {
		t.Fatal("new event bus should not have subscribers")
	}

	// 1. 订阅
	ch1 := eb.Subscribe()
	ch2 := eb.Subscribe()
	if !eb.HasSubscribers() {
		t.Fatal("event bus should report subscribers after Subscribe")
	}

	// 2. 发布事件
	eb.PublishJSON("test_event", map[string]string{"foo": "bar"})

	// 3. 验证接收
	checkRecv := func(ch <-chan SSEEvent, name, expectedType, expectedData string) {
		select {
		case ev := <-ch:
			if ev.Type != expectedType {
				t.Errorf("%s expected to receive %s, got %s", name, expectedType, ev.Type)
			}
			if !strings.Contains(ev.Data, expectedData) {
				t.Errorf("%s data mismatch: %s", name, ev.Data)
			}
		case <-time.After(500 * time.Millisecond):
			t.Errorf("%s did not receive event", name)
		}
	}

	checkRecv(ch1, "ch1", "test_event", `"foo":"bar"`)
	checkRecv(ch2, "ch2", "test_event", `"foo":"bar"`)

	// 4. 退订
	eb.Unsubscribe(ch1)
	if !eb.HasSubscribers() {
		t.Fatal("event bus should still have one subscriber after unsubscribing ch1")
	}

	// 验证退订后的通道不应再收到新事件
	eb.PublishJSON("hello", "world")
	checkRecv(ch2, "ch2", "hello", `"world"`)

	select {
	case ev, ok := <-ch1:
		if ok {
			t.Errorf("ch1 already unsubscribed, should not receive valid events: %v", ev)
		}
	case <-time.After(50 * time.Millisecond):
		// 正常，没有事件
		// 正常，没有事件
	}

	eb.Unsubscribe(ch2)
	if eb.HasSubscribers() {
		t.Fatal("event bus should not have subscribers after all channels unsubscribe")
	}
}

func TestEventBus_PublishTimeout(t *testing.T) {
	eb := NewEventBus()

	// 订阅一个通道但故意不读
	ch := eb.Subscribe()

	// 连续发送超过缓冲区 (100) 的消息，触发 Publish 的 select default 分支
	// 这里发 150 个
	for i := 0; i < 150; i++ {
		eb.Publish(SSEEvent{Type: "spam"})
	}

	// 检查通道里面应该只有 64 个
	count := 0
loop:
	for {
		select {
		case <-ch:
			count++
		default:
			break loop
		}
	}

	if count != 64 {
		t.Errorf("expected channel to be full with 64, got %d", count)
	}
}

func TestSSEConnectionRegistryTargetsSessionAndUser(t *testing.T) {
	s := New(0)
	firstContext, firstCancel := context.WithCancel(context.Background())
	secondContext, secondCancel := context.WithCancel(context.Background())
	otherContext, otherCancel := context.WithCancel(context.Background())
	defer firstCancel()
	defer secondCancel()
	defer otherCancel()

	registry := s.getSSEConnectionRegistry()
	firstRelease := registry.register("user-a", "session-a-1", firstCancel)
	secondRelease := registry.register("user-a", "session-a-2", secondCancel)
	otherRelease := registry.register("user-b", "session-b-1", otherCancel)
	defer firstRelease()
	defer secondRelease()
	defer otherRelease()

	s.cancelSSEForSession("session-a-1", "test")
	select {
	case <-firstContext.Done():
	case <-time.After(time.Second):
		t.Fatal("session cancellation did not close its SSE context")
	}
	select {
	case <-secondContext.Done():
		t.Fatal("session cancellation closed another session for the same user")
	case <-otherContext.Done():
		t.Fatal("session cancellation closed another user")
	default:
	}

	s.cancelSSEForUser("user-a", "test")
	select {
	case <-secondContext.Done():
	case <-time.After(time.Second):
		t.Fatal("user cancellation did not close remaining user SSE context")
	}
	select {
	case <-otherContext.Done():
		t.Fatal("user cancellation closed another user")
	default:
	}
}

func TestHandleSSE_DisconnectCleanup(t *testing.T) {
	s := New(0)
	// mock auth: SSE 不需要认证 (实际中前面会有 RequireAuth)，这里直接调 handleSSE

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req = req.WithContext(ctx)

	// 为了拦截 writer，我们手写个 response recorder，支持 closeNotify (虽然 http.ResponseWriter 已经不再推荐，但在测试请求中断时，Cancel / Context Done 是主要方式)
	w := newLockedRecorder()

	// 启动 handleSSE 会阻塞，所以放进 goroutine
	done := make(chan struct{})
	go func() {
		s.handleSSE(w, req)
		close(done)
	}()

	// 确认订阅数增加
	time.Sleep(50 * time.Millisecond)
	s.events.mu.RLock()
	subCount := len(s.events.subscribers)
	s.events.mu.RUnlock()
	if subCount != 1 {
		t.Errorf("expected one subscriber, got %d", subCount)
	}

	body := w.BodyString()
	if !strings.Contains(body, "event: ready\ndata: {\"activity_cursor\":0}\n\n") {
		t.Fatalf("expected ready activity cursor immediately after SSE connection, actual body: %q", body)
	}

	if strings.Contains(body, "event: snapshot\n") {
		t.Fatalf("administrator-global SSE must not generate snapshots, actual body: %q", body)
	}

	// Administrator-global streams carry durable activity hints and ephemeral
	// user-list refresh hints; resource events stay on the selected user's stream.
	s.events.PublishJSON("client_online", "hidden")
	s.events.PublishJSON("activity_event", "bar")
	s.events.PublishJSON("user_list_changed", map[string]string{"action": "deleted", "user_id": "user-a"})
	time.Sleep(50 * time.Millisecond)

	body = w.BodyString()
	if strings.Contains(body, "hidden") {
		t.Fatalf("administrator-global SSE received a resource event: %q", body)
	}
	if !strings.Contains(body, "event: activity_event\ndata: \"bar\"\n\n") {
		t.Fatalf("administrator-global SSE missed an activity event: %q", body)
	}
	if !strings.Contains(body, "event: user_list_changed\n") || !strings.Contains(body, `"user_id":"user-a"`) {
		t.Fatalf("administrator-global SSE missed a user-list refresh event: %q", body)
	}

	// 模拟客户端断开连接 (Cancel context)
	cancel()

	// 等待 handleSSE 退出
	select {
	case <-done:
		// 正常退出
	case <-time.After(1 * time.Second):
		t.Fatal("handleSSE did not exit correctly when client disconnected")
	}

	// 确认订阅数减少为 0
	s.events.mu.RLock()
	subCount = len(s.events.subscribers)
	s.events.mu.RUnlock()
	if subCount != 0 {
		t.Errorf("subscription should be cleaned up after client disconnect, remaining: %d", subCount)
	}
}

func TestSSEReadScopeKeepsUserListRefreshGlobal(t *testing.T) {
	global := sseReadScope{global: true}
	user := sseReadScope{userID: "user-a"}
	listChanged := SSEEvent{Type: "user_list_changed"}
	resourceEvent := SSEEvent{Type: "client_online", ScopeUserID: "user-a"}

	if !global.allows(listChanged) {
		t.Fatal("administrator-global scope must receive user-list refresh events")
	}
	if user.allows(listChanged) {
		t.Fatal("user-scoped stream must not receive administrator-global user-list refresh events")
	}
	if global.allows(resourceEvent) {
		t.Fatal("administrator-global scope must not receive user resource events")
	}
	if !user.allows(resourceEvent) {
		t.Fatal("matching user scope must receive its resource events")
	}
}

func TestHandleSSE_UserScopeKeepsSnapshot(t *testing.T) {
	s := New(0)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req = req.WithContext(context.WithValue(req.Context(), resourceScopeContextKey{}, ResourceScope{OwnerUserID: "user-a"}))
	w := newLockedRecorder()
	done := make(chan struct{})
	go func() {
		s.handleSSE(w, req)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for !strings.Contains(w.BodyString(), "event: snapshot\n") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	body := w.BodyString()
	if !strings.Contains(body, "event: snapshot\n") || !strings.Contains(body, `"bootstrap":`) {
		cancel()
		t.Fatalf("user-scoped SSE should receive a scoped bootstrap snapshot, actual body: %q", body)
	}
	if strings.Contains(body, `"server_status":`) {
		cancel()
		t.Fatalf("user-scoped SSE must not expose server status, actual body: %q", body)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scoped SSE did not stop")
	}
}

func TestHandleSSE_UserScopeRejectsCapturedStaleEpoch(t *testing.T) {
	s := New(0)
	gate := s.lifecycleGate("user-a")
	gate.mu.Lock()
	expectedEpoch := gate.epoch
	gate.epoch++
	gate.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req = req.WithContext(context.WithValue(req.Context(), resourceScopeContextKey{}, ResourceScope{
		OwnerUserID:   "user-a",
		ExpectedEpoch: expectedEpoch,
	}))
	w := newLockedRecorder()
	s.handleSSE(w, req)

	if w.rec.Code != http.StatusConflict {
		t.Fatalf("stale scoped SSE status = %d, want %d; body=%q", w.rec.Code, http.StatusConflict, w.BodyString())
	}
	if !strings.Contains(w.BodyString(), `"code":"user_lifecycle_changed"`) {
		t.Fatalf("stale scoped SSE response = %q, want lifecycle error", w.BodyString())
	}
}

func TestHandleSSESubscribesBeforeActivityCursorRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), serverDBFileName)
	db, err := openServerDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := New(0)
	s.serverDB = db
	s.activityStore = newActivityStoreWithDB(path, db, false)
	_, err = s.activityStore.Append(testActivitySpec("created", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newLockedRecorder()
	done := make(chan struct{})
	go func() {
		s.handleSSE(w, httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx))
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for !s.events.HasSubscribers() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.events.HasSubscribers() {
		t.Fatal("SSE did not subscribe")
	}
	secondID, err := s.activityStore.Append(testActivitySpec("updated", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	s.publishActivityID(secondID)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		body := w.BodyString()
		if strings.Contains(body, fmt.Sprintf(`"activity_cursor":%d`, secondID)) || strings.Contains(body, fmt.Sprintf(`"id":%d`, secondID)) {
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("SSE did not stop")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("event committed after subscription was outside cursor and hint: %s", w.BodyString())
}
