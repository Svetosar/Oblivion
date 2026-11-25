// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// putVersioned updates an item with the version it currently has, as an editor
// that has just loaded it would. For tests whose subject is not the version.
func (e *env) putVersioned(user, path string, body map[string]any) *httptest.ResponseRecorder {
	e.t.Helper()
	got := map[string]any{}
	if err := json.Unmarshal(e.do("admin:1:root", "GET", path, nil).Body.Bytes(), &got); err != nil {
		e.t.Fatal(err)
	}
	b := map[string]any{"version": got["version"]}
	for k, v := range body {
		b[k] = v
	}
	return e.do(user, "PUT", path, b)
}

// versionOf reads the version of a decoded item; -1 if the field is absent.
func versionOf(m map[string]any) int64 {
	f, ok := m["version"].(float64)
	if !ok {
		return -1
	}
	return int64(f)
}

func (e *env) item(path string) map[string]any {
	e.t.Helper()
	w := e.do("admin:1:root", "GET", path, nil)
	want(e.t, w, 200)
	m := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func edit(title string, version any) map[string]any {
	b := map[string]any{"title": title, "body_md": "text of " + title, "status": "draft"}
	if version != nil {
		b["version"] = version
	}
	return b
}

func TestNewItemStartsAtVersionOne(t *testing.T) {
	e := newEnv(t)
	it := e.create("admin:1:root", "post", "A", "draft", nil)
	if versionOf(it) != 1 {
		t.Fatalf("version = %v, want 1", it["version"])
	}
}

func TestUpdateRequiresTheLoadedVersion(t *testing.T) {
	e := newEnv(t)
	path := fmt.Sprintf("/api/v1/content/items/%d", id(e.create("admin:1:root", "post", "A", "draft", nil)))
	// Without it a client would silently overwrite whatever is there: refuse.
	want(t, e.do("admin:1:root", "PUT", path, edit("B", nil)), 428)
	want(t, e.do("admin:1:root", "PUT", path, edit("B", 0)), 428)
	want(t, e.do("admin:1:root", "PUT", path, edit("B", -3)), 428)
	if got := e.item(path)["title"]; got != "A" {
		t.Fatalf("title changed to %v by a rejected update", got)
	}
}

func TestStaleUpdateIsRejectedAndChangesNothing(t *testing.T) {
	e := newEnv(t)
	path := fmt.Sprintf("/api/v1/content/items/%d", id(e.create("admin:1:root", "post", "A", "draft", nil)))
	// The clock is frozen on purpose: updated_at cannot tell these saves apart.

	// Two editors load version 1; the first saves.
	w := e.do("admin:1:root", "PUT", path, edit("first", 1))
	want(t, w, 200)
	var saved map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if versionOf(saved) != 2 {
		t.Fatalf("version after save = %v, want 2", saved["version"])
	}

	// The second still holds version 1.
	want(t, e.do("admin:1:root", "PUT", path, edit("second", 1)), 412)
	now := e.item(path)
	if now["title"] != "first" || versionOf(now) != 2 {
		t.Fatalf("a rejected update changed the item: %v", now)
	}
	// A rejected update must not leave the public site stale either.
	want(t, e.do("admin:1:root", "PUT", path, edit("third", 2)), 200)
}

func TestVersionChainsAcrossSaves(t *testing.T) {
	e := newEnv(t)
	path := fmt.Sprintf("/api/v1/content/items/%d", id(e.create("admin:1:root", "post", "A", "draft", nil)))
	for v := int64(1); v <= 5; v++ {
		w := e.do("admin:1:root", "PUT", path, edit(fmt.Sprintf("v%d", v), v))
		want(t, w, 200)
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		if versionOf(m) != v+1 {
			t.Fatalf("after save %d the version is %v", v, m["version"])
		}
	}
}

func TestUpdateOfDeletedItemIsNotFoundNotConflict(t *testing.T) {
	e := newEnv(t)
	path := fmt.Sprintf("/api/v1/content/items/%d", id(e.create("admin:1:root", "post", "A", "draft", nil)))
	want(t, e.do("admin:1:root", "DELETE", path, nil), 204)
	want(t, e.do("admin:1:root", "PUT", path, edit("B", 1)), 404)
}

// The conflict check must not leak whether a stale version belongs to an item
// the caller may not touch: permissions are decided first.
func TestPermissionIsCheckedBeforeVersion(t *testing.T) {
	e := newEnv(t)
	mine := e.create("author:7:alice", "post", "Mine", "draft", nil)
	theirs := e.create("editor:2:ed", "post", "Theirs", "draft", nil)
	want(t, e.do("author:7:alice", "PUT", fmt.Sprintf("/api/v1/content/items/%d", id(theirs)), edit("X", 99)), 403)
	want(t, e.do("author:7:alice", "PUT", fmt.Sprintf("/api/v1/content/items/%d", id(mine)), edit("X", 99)), 412)
}

// Two requests carrying the same version race: the check and the write must be
// one atomic step, or both would win.
func TestConcurrentUpdatesWithOneVersionHaveOneWinner(t *testing.T) {
	e := newEnv(t)
	path := fmt.Sprintf("/api/v1/content/items/%d", id(e.create("admin:1:root", "post", "A", "draft", nil)))
	const n = 16
	var wins, conflicts, other atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			switch e.do("admin:1:root", "PUT", path, edit(fmt.Sprintf("writer %d", i), 1)).Code {
			case http.StatusOK:
				wins.Add(1)
			case http.StatusPreconditionFailed:
				conflicts.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != n-1 || other.Load() != 0 {
		t.Fatalf("wins=%d conflicts=%d other=%d, want 1/%d/0", wins.Load(), conflicts.Load(), other.Load(), n-1)
	}
	if v := versionOf(e.item(path)); v != 2 {
		t.Fatalf("version = %d, want 2", v)
	}
}

// Rows that existed before the column was added count as version 1.
func TestExistingRowsGetVersionOne(t *testing.T) {
	e := newEnv(t)
	_, err := e.p.db.Exec(`INSERT INTO content_items(kind, slug, title, body_md, body_html, status, author_id, author_name, created_at, updated_at)
		VALUES ('post','old','Old','x','<p>x</p>','draft',1,'a',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	var v int64
	if err := e.p.db.QueryRow(`SELECT version FROM content_items WHERE slug='old'`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("version = %d, err %v", v, err)
	}
}
