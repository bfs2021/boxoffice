package api

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yeixio/boxoffice/internal/homemedia"
	"github.com/yeixio/boxoffice/internal/mediatypes"
	"github.com/yeixio/boxoffice/internal/store"
)

// Home videos and photos: what's in the home media root folders, by date,
// in albums, and the copies found. See package homemedia.
func (s *Server) homeRoutes() []route {
	return []route{
		{"GET /api/v1/home", true, s.homeSummary},
		{"POST /api/v1/home/scan", true, s.startHomeScan},
		{"GET /api/v1/home/items", true, s.homeItems},
		{"GET /api/v1/home/months", true, s.homeMonths},
		{"GET /api/v1/home/items/{id}", true, s.homeItem},
		{"GET /api/v1/home/items/{id}/thumb", true, s.homeThumb},
		{"GET /api/v1/home/items/{id}/view", true, s.homeView},
		{"GET /api/v1/home/items/{id}/file", true, s.homeFile},
		{"GET /api/v1/home/albums", true, s.homeAlbums},
		{"POST /api/v1/home/albums", true, s.createHomeAlbum},
		{"GET /api/v1/home/albums/{id}", true, s.homeAlbum},
		{"PATCH /api/v1/home/albums/{id}", true, s.editHomeAlbum},
		{"DELETE /api/v1/home/albums/{id}", true, s.deleteHomeAlbum},
		{"POST /api/v1/home/albums/{id}/items", true, s.addToHomeAlbum},
		{"POST /api/v1/home/albums/{id}/remove", true, s.removeFromHomeAlbum},
		{"GET /api/v1/home/duplicates", true, s.homeDupes},
		{"POST /api/v1/home/duplicates/recycle", true, s.recycleHomeDupes},
	}
}

func (s *Server) homeOn(r *http.Request) error {
	if s.Home == nil {
		return errorf(http.StatusConflict, homemedia.ErrOff.Error())
	}
	if err := s.Home.On(r.Context()); err != nil {
		if errors.Is(err, homemedia.ErrOff) {
			return errorf(http.StatusConflict, err.Error())
		}
		return err
	}
	return nil
}

type homeSummary struct {
	store.HomeTotals
	Roots  int                `json:"roots"`
	Albums int                `json:"albums"`
	Scan   homemedia.Progress `json:"scan"`
}

func (s *Server) homeSummary(w http.ResponseWriter, r *http.Request) error {
	if err := s.homeOn(r); err != nil {
		return err
	}
	t, err := s.Store.HomeTotals(r.Context())
	if err != nil {
		return err
	}
	roots, err := s.Store.RootFolders(r.Context(), mediatypes.Home)
	if err != nil {
		return err
	}
	albums, err := s.Store.HomeAlbums(r.Context(), false)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, homeSummary{HomeTotals: t, Roots: len(roots), Albums: len(albums), Scan: s.Home.Progress()})
}

func (s *Server) startHomeScan(w http.ResponseWriter, r *http.Request) error {
	if err := s.homeOn(r); err != nil {
		return err
	}
	p, err := s.Home.Start(r.Context())
	if errors.Is(err, homemedia.ErrBusy) {
		return errorf(http.StatusConflict, err.Error())
	}
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusAccepted, p)
}

// homeItemView is a photo or video with what the page needs to show it.
type homeItemView struct {
	store.HomeItem
	Name     string `json:"name"`
	Folder   string `json:"folder"`
	HasThumb bool   `json:"hasThumb"`
	Viewable bool   `json:"viewable"`
}

func itemView(it store.HomeItem) homeItemView {
	rel, err := filepath.Rel(it.RootFolder, filepath.Dir(it.Path))
	if err != nil || rel == "." {
		rel = ""
	}
	return homeItemView{HomeItem: it, Name: filepath.Base(it.Path), Folder: rel, HasThumb: it.Thumb == 1,
		Viewable: it.Kind == "photo" || homemedia.Viewable(it)}
}

func homeKind(r *http.Request) (string, error) {
	k := r.URL.Query().Get("kind")
	if k != "" && k != "photo" && k != "video" {
		return "", errorf(http.StatusBadRequest, "kind is photo or video")
	}
	return k, nil
}

// homeItems lists photos and videos newest first, a page at a time:
// before is the next value of the page before.
func (s *Server) homeItems(w http.ResponseWriter, r *http.Request) error {
	if err := s.homeOn(r); err != nil {
		return err
	}
	q := store.HomeQuery{}
	var err error
	if q.Kind, err = homeKind(r); err != nil {
		return err
	}
	q.AlbumID, _ = strconv.ParseInt(r.URL.Query().Get("album"), 10, 64)
	q.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if b := r.URL.Query().Get("before"); b != "" {
		taken, id, ok := strings.Cut(b, ":")
		t, err1 := strconv.ParseInt(taken, 10, 64)
		i, err2 := strconv.ParseInt(id, 10, 64)
		if !ok || err1 != nil || err2 != nil {
			return errorf(http.StatusBadRequest, "before is taken:id")
		}
		q.BeforeTaken, q.BeforeID = t, i
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 200
	}
	list, err := s.Store.HomeTimeline(r.Context(), q)
	if err != nil {
		return err
	}
	out := struct {
		Items []homeItemView `json:"items"`
		Next  string         `json:"next,omitempty"`
	}{Items: make([]homeItemView, 0, len(list))}
	for _, it := range list {
		out.Items = append(out.Items, itemView(it))
	}
	if len(list) == q.Limit {
		last := list[len(list)-1]
		out.Next = fmt.Sprintf("%d:%d", last.Taken, last.ID)
	}
	return writeJSON(w, http.StatusOK, out)
}

// homeMonths counts the photos and videos taken each month, newest first,
// for jumping through the timeline.
func (s *Server) homeMonths(w http.ResponseWriter, r *http.Request) error {
	if err := s.homeOn(r); err != nil {
		return err
	}
	kind, err := homeKind(r)
	if err != nil {
		return err
	}
	album, _ := strconv.ParseInt(r.URL.Query().Get("album"), 10, 64)
	taken, err := s.Store.HomeTaken(r.Context(), kind, album)
	if err != nil {
		return err
	}
	type month struct {
		Month string `json:"month"` // 2024-07
		Count int    `json:"count"`
	}
	out := []month{}
	for _, t := range taken {
		m := time.Unix(t, 0).UTC().Format("2006-01")
		if n := len(out); n > 0 && out[n-1].Month == m {
			out[n-1].Count++
			continue
		}
		out = append(out, month{Month: m, Count: 1})
	}
	return writeJSON(w, http.StatusOK, out)
}

func (s *Server) homeItemByID(r *http.Request) (store.HomeItem, error) {
	if err := s.homeOn(r); err != nil {
		return store.HomeItem{}, err
	}
	id, err := pathID(r, "id")
	if err != nil {
		return store.HomeItem{}, err
	}
	return s.Store.HomeItem(r.Context(), id)
}

func (s *Server) homeItem(w http.ResponseWriter, r *http.Request) error {
	it, err := s.homeItemByID(r)
	if err != nil {
		return err
	}
	albums, err := s.Store.HomeAlbumsOf(r.Context(), it.ID)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, struct {
		homeItemView
		Albums []store.HomeAlbum `json:"albums"`
	}{itemView(it), albums})
}

func (s *Server) homeThumb(w http.ResponseWriter, r *http.Request) error {
	it, err := s.homeItemByID(r)
	if err != nil {
		return err
	}
	p, err := s.Home.Thumb(it)
	if err != nil {
		return errorf(http.StatusNotFound, err.Error())
	}
	// Named for the file's bytes, so it never changes.
	w.Header().Set("Cache-Control", "private, max-age=2592000, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, p)
	return nil
}

// homeView shows a photo or plays a video in the browser: the file itself,
// or for photos browsers can't show, a JPEG made from it.
func (s *Server) homeView(w http.ResponseWriter, r *http.Request) error {
	it, err := s.homeItemByID(r)
	if err != nil {
		return err
	}
	p, err := s.Home.View(r.Context(), it)
	if err != nil {
		s.Log.Debug("can't show a home photo", "path", it.Path, "err", err)
		return errorf(http.StatusUnprocessableEntity, "Box Office can't show this one: "+err.Error())
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if p != it.Path {
		w.Header().Set("Content-Type", "image/jpeg")
	}
	// ServeFile answers range requests, for seeking in videos.
	http.ServeFile(w, r, p)
	return nil
}

// homeFile downloads the original file.
func (s *Server) homeFile(w http.ResponseWriter, r *http.Request) error {
	it, err := s.homeItemByID(r)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(it.Path)))
	http.ServeFile(w, r, it.Path)
	return nil
}

func (s *Server) homeAlbums(w http.ResponseWriter, r *http.Request) error {
	if err := s.homeOn(r); err != nil {
		return err
	}
	list, err := s.Store.HomeAlbums(r.Context(), r.URL.Query().Get("hidden") == "true")
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, list)
}

func (s *Server) homeAlbum(w http.ResponseWriter, r *http.Request) error {
	if err := s.homeOn(r); err != nil {
		return err
	}
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	a, err := s.Store.HomeAlbum(r.Context(), id)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, a)
}

func (s *Server) createHomeAlbum(w http.ResponseWriter, r *http.Request) error {
	if err := s.homeOn(r); err != nil {
		return err
	}
	var in struct {
		Name  string  `json:"name"`
		Items []int64 `json:"items"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return errorf(http.StatusBadRequest, "give the album a name")
	}
	a, err := s.Store.CreateHomeAlbum(r.Context(), name)
	if err != nil {
		return err
	}
	if err := s.Store.AddToHomeAlbum(r.Context(), a.ID, in.Items); err != nil {
		return err
	}
	a, err = s.Store.HomeAlbum(r.Context(), a.ID)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusCreated, a)
}

func (s *Server) editHomeAlbum(w http.ResponseWriter, r *http.Request) error {
	if err := s.homeOn(r); err != nil {
		return err
	}
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in store.HomeAlbumEdit
	if err := readJSON(r, &in); err != nil {
		return err
	}
	a, err := s.Store.HomeAlbum(r.Context(), id)
	if err != nil {
		return err
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" && a.Kind == "own" {
		return errorf(http.StatusBadRequest, "give the album a name")
	}
	if err := s.Store.EditHomeAlbum(r.Context(), id, in); err != nil {
		return err
	}
	a, err = s.Store.HomeAlbum(r.Context(), id)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, a)
}

// deleteHomeAlbum deletes an album of one's own. One Box Office made is
// put away instead, so it isn't made again. The photos stay either way.
func (s *Server) deleteHomeAlbum(w http.ResponseWriter, r *http.Request) error {
	if err := s.homeOn(r); err != nil {
		return err
	}
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	a, err := s.Store.HomeAlbum(r.Context(), id)
	if err != nil {
		return err
	}
	if a.Kind == "event" {
		hidden := true
		err = s.Store.EditHomeAlbum(r.Context(), id, store.HomeAlbumEdit{Hidden: &hidden})
	} else {
		err = s.Store.DeleteHomeAlbum(r.Context(), id)
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func readItems(r *http.Request) ([]int64, error) {
	var in struct {
		Items []int64 `json:"items"`
	}
	if err := readJSON(r, &in); err != nil {
		return nil, err
	}
	if len(in.Items) == 0 {
		return nil, errorf(http.StatusBadRequest, "pick some photos or videos first")
	}
	if len(in.Items) > 5000 {
		return nil, errorf(http.StatusBadRequest, "pick 5,000 or fewer at a time")
	}
	return in.Items, nil
}

func (s *Server) addToHomeAlbum(w http.ResponseWriter, r *http.Request) error {
	if err := s.homeOn(r); err != nil {
		return err
	}
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	items, err := readItems(r)
	if err != nil {
		return err
	}
	if err := s.Store.AddToHomeAlbum(r.Context(), id, items); err != nil {
		return err
	}
	a, err := s.Store.HomeAlbum(r.Context(), id)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, a)
}

func (s *Server) removeFromHomeAlbum(w http.ResponseWriter, r *http.Request) error {
	if err := s.homeOn(r); err != nil {
		return err
	}
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	items, err := readItems(r)
	if err != nil {
		return err
	}
	if _, err := s.Store.HomeAlbum(r.Context(), id); err != nil {
		return err
	}
	if err := s.Store.RemoveFromHomeAlbum(r.Context(), id, items); err != nil {
		return err
	}
	a, err := s.Store.HomeAlbum(r.Context(), id)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, a)
}

func (s *Server) homeDupes(w http.ResponseWriter, r *http.Request) error {
	if err := s.homeOn(r); err != nil {
		return err
	}
	groups, err := s.Home.Dupes(r.Context())
	if err != nil {
		return err
	}
	type group struct {
		Kind  string         `json:"kind"`
		Items []homeItemView `json:"items"`
		Keep  int64          `json:"keep"`
		Spare int64          `json:"spare"`
	}
	out := make([]group, 0, len(groups))
	for _, g := range groups {
		v := group{Kind: g.Kind, Keep: g.Keep, Spare: g.Spare}
		for _, it := range g.Items {
			v.Items = append(v.Items, itemView(it))
		}
		out = append(out, v)
	}
	return writeJSON(w, http.StatusOK, out)
}

// recycleHomeDupes moves copies picked on the Copies tab into their root
// folder's .recycle folder. size is what the page showed they take.
func (s *Server) recycleHomeDupes(w http.ResponseWriter, r *http.Request) error {
	if err := s.homeOn(r); err != nil {
		return err
	}
	var in struct {
		Items []int64 `json:"items"`
		Size  int64   `json:"size"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if len(in.Items) == 0 {
		return errorf(http.StatusBadRequest, "pick the copies to move first")
	}
	n, freed, err := s.Home.RecycleCopies(r.Context(), in.Items, in.Size)
	var re homemedia.RecycleError
	if errors.As(err, &re) {
		return errorf(http.StatusConflict, re.Error())
	}
	if err != nil && n == 0 {
		return err
	}
	out := struct {
		Moved int    `json:"moved"`
		Freed int64  `json:"freed"`
		Error string `json:"error,omitempty"`
	}{Moved: n, Freed: freed}
	if err != nil {
		out.Error = err.Error()
	}
	return writeJSON(w, http.StatusOK, out)
}
