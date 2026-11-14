package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/yeixio/boxoffice/internal/homemedia"
	"github.com/yeixio/boxoffice/internal/mediatypes"
	"github.com/yeixio/boxoffice/internal/store"
)

// The Home videos and photos pages answer only once it's on; the timeline
// comes a page at a time, newest first, and albums of one's own can be
// made, filled and deleted, while Box Office's own are only put away.
func TestHomePages(t *testing.T) {
	s, hs, _ := monitorServer(t)
	ctx := context.Background()
	s.Home = &homemedia.Service{Store: s.Store, Dir: t.TempDir(), Places: "off"}
	if code := call(t, hs, "GET", "/api/v1/home", "", nil); code != http.StatusConflict {
		t.Fatalf("home off: %d", code)
	}
	mediatypes.SetEnabled(ctx, s.Store, mediatypes.Home, true)
	var ids []int64
	for i := range 5 {
		id, err := s.Store.PutHomeItem(ctx, store.HomeItem{RootFolder: "/photos", Path: fmt.Sprintf("/photos/2024/IMG_%d.jpg", i), Kind: "photo",
			Size: 100, Taken: int64(1720000000 + i*86400*20), Thumb: 1})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	s.Store.PutHomeItem(ctx, store.HomeItem{RootFolder: "/photos", Path: "/photos/clip.mov", Kind: "video", Size: 500, Taken: 1700000000})

	var sum homeSummary
	if code := call(t, hs, "GET", "/api/v1/home", "", &sum); code != 200 || sum.Photos != 5 || sum.Videos != 1 || sum.Size != 1000 {
		t.Fatalf("summary %d %+v", code, sum)
	}
	var page struct {
		Items []homeItemView `json:"items"`
		Next  string         `json:"next"`
	}
	call(t, hs, "GET", "/api/v1/home/items?limit=4", "", &page)
	if len(page.Items) != 4 || page.Items[0].Name != "IMG_4.jpg" || page.Items[0].Folder != "2024" || page.Next == "" {
		t.Fatalf("page 1 %+v", page)
	}
	next := page.Next
	page.Next = ""
	call(t, hs, "GET", "/api/v1/home/items?limit=4&before="+next, "", &page)
	if len(page.Items) != 2 || page.Items[1].Kind != "video" || page.Next != "" {
		t.Fatalf("page 2 %+v", page)
	}
	call(t, hs, "GET", "/api/v1/home/items?kind=video", "", &page)
	if len(page.Items) != 1 || !page.Items[0].Viewable {
		t.Fatalf("videos %+v", page)
	}
	var months []struct {
		Month string `json:"month"`
		Count int    `json:"count"`
	}
	call(t, hs, "GET", "/api/v1/home/months", "", &months)
	if len(months) < 3 || months[len(months)-1].Month != "2023-11" {
		t.Fatalf("months %+v", months)
	}

	// An album of one's own.
	var a store.HomeAlbum
	if code := call(t, hs, "POST", "/api/v1/home/albums", `{"name":" "}`, nil); code != 400 {
		t.Fatalf("unnamed album: %d", code)
	}
	body := fmt.Sprintf(`{"name":"Garden","items":[%d,%d]}`, ids[0], ids[1])
	if code := call(t, hs, "POST", "/api/v1/home/albums", body, &a); code != 201 || a.Count != 2 || a.CoverID != ids[0] {
		t.Fatalf("album %d %+v", code, a)
	}
	call(t, hs, "POST", fmt.Sprintf("/api/v1/home/albums/%d/items", a.ID), fmt.Sprintf(`{"items":[%d,%d]}`, ids[1], ids[2]), &a)
	if a.Count != 3 {
		t.Fatalf("after adding %+v", a)
	}
	call(t, hs, "POST", fmt.Sprintf("/api/v1/home/albums/%d/remove", a.ID), fmt.Sprintf(`{"items":[%d]}`, ids[0]), &a)
	if a.Count != 2 {
		t.Fatalf("after removing %+v", a)
	}
	call(t, hs, "GET", fmt.Sprintf("/api/v1/home/items?album=%d", a.ID), "", &page)
	if len(page.Items) != 2 {
		t.Fatalf("album items %+v", page)
	}
	var detail struct {
		Albums []store.HomeAlbum `json:"albums"`
	}
	call(t, hs, "GET", fmt.Sprintf("/api/v1/home/items/%d", ids[1]), "", &detail)
	if len(detail.Albums) != 1 || detail.Albums[0].Name != "Garden" {
		t.Fatalf("item albums %+v", detail)
	}
	if code := call(t, hs, "DELETE", fmt.Sprintf("/api/v1/home/albums/%d", a.ID), "", nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code := call(t, hs, "GET", fmt.Sprintf("/api/v1/home/albums/%d", a.ID), "", nil); code != 404 {
		t.Fatalf("deleted album: %d", code)
	}
	// An event album is put away, not deleted.
	s.Store.SyncHomeEvents(ctx, []store.HomeEvent{{Items: ids, Start: 1, End: 2}})
	var albums []store.HomeAlbum
	call(t, hs, "GET", "/api/v1/home/albums", "", &albums)
	if len(albums) != 1 || albums[0].Kind != "event" {
		t.Fatalf("events %+v", albums)
	}
	call(t, hs, "DELETE", fmt.Sprintf("/api/v1/home/albums/%d", albums[0].ID), "", nil)
	call(t, hs, "GET", "/api/v1/home/albums", "", &albums)
	if len(albums) != 0 {
		t.Fatalf("after putting away %+v", albums)
	}
	call(t, hs, "GET", "/api/v1/home/albums?hidden=true", "", &albums)
	if len(albums) != 1 || !albums[0].Hidden {
		t.Fatalf("put away %+v", albums)
	}
	// Moving every copy of something is refused.
	if code := call(t, hs, "POST", "/api/v1/home/duplicates/recycle", fmt.Sprintf(`{"items":[%d],"size":100}`, ids[0]), nil); code != 409 {
		t.Fatalf("recycle a photo that isn't a copy: %d", code)
	}
}
