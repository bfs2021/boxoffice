package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// HomeItem is a photo or video found in a home media root folder.
type HomeItem struct {
	ID         int64  `json:"id"`
	RootFolder string `json:"rootFolder"`
	Path       string `json:"path"`
	Kind       string `json:"kind"` // photo or video
	Size       int64  `json:"size"`
	MTime      int64  `json:"-"`
	// Taken is the clock time it was taken, as Unix seconds read as UTC:
	// 14:30 on a camera is 14:30 here, wherever it was. 0 is unknown.
	Taken       int64   `json:"taken"`
	TakenFrom   string  `json:"takenFrom,omitempty"` // camera, video, name or file
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	Duration    float64 `json:"duration,omitempty"`
	Make        string  `json:"make,omitempty"`
	Model       string  `json:"model,omitempty"`
	Lat         float64 `json:"lat,omitempty"`
	Lon         float64 `json:"lon,omitempty"`
	HasGPS      bool    `json:"hasGps,omitempty"`
	Place       string  `json:"place,omitempty"`
	Orientation int     `json:"-"`
	Sum         string  `json:"-"`
	Look        string  `json:"-"`
	Thumb       int     `json:"-"`
	AddedAt     int64   `json:"-"`
}

const homeItemCols = `id, root_folder, path, kind, size, mtime, taken_at, taken_from, width, height, duration,
	make, model, lat, lon, has_gps, place, orientation, sum, look, thumb, added_at`

func scanHomeItem(r interface{ Scan(...any) error }) (HomeItem, error) {
	var it HomeItem
	var gps int
	err := r.Scan(&it.ID, &it.RootFolder, &it.Path, &it.Kind, &it.Size, &it.MTime, &it.Taken, &it.TakenFrom, &it.Width, &it.Height, &it.Duration,
		&it.Make, &it.Model, &it.Lat, &it.Lon, &gps, &it.Place, &it.Orientation, &it.Sum, &it.Look, &it.Thumb, &it.AddedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return it, ErrNotFound
	}
	it.HasGPS = gps == 1
	return it, err
}

func (s *Store) homeItems(ctx context.Context, where string, args ...any) ([]HomeItem, error) {
	rows, err := s.query(ctx, "SELECT "+homeItemCols+" FROM home_items "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HomeItem{}
	for rows.Next() {
		it, err := scanHomeItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// HomeItem finds one photo or video.
func (s *Store) HomeItem(ctx context.Context, id int64) (HomeItem, error) {
	return scanHomeItem(s.row(ctx, "SELECT "+homeItemCols+" FROM home_items WHERE id = ?", id))
}

// HomeItemsIn lists the photos and videos found in a root folder.
func (s *Store) HomeItemsIn(ctx context.Context, root string) ([]HomeItem, error) {
	return s.homeItems(ctx, "WHERE root_folder = ?", root)
}

// AllHomeItems lists every photo and video, newest first.
func (s *Store) AllHomeItems(ctx context.Context) ([]HomeItem, error) {
	return s.homeItems(ctx, "ORDER BY taken_at DESC, id DESC")
}

// HomeItemsByID lists the photos and videos with these ids.
func (s *Store) HomeItemsByID(ctx context.Context, ids []int64) ([]HomeItem, error) {
	if len(ids) == 0 {
		return []HomeItem{}, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return s.homeItems(ctx, "WHERE id IN ("+placeholders(len(ids))+") ORDER BY taken_at DESC, id DESC", args...)
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// PutHomeItem saves what was read from a file: a new row for a path not
// seen before, or the same row again for one that changed.
func (s *Store) PutHomeItem(ctx context.Context, it HomeItem) (int64, error) {
	var id int64
	err := s.row(ctx, "SELECT id FROM home_items WHERE path = ?", it.Path).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return s.insert(ctx, `INSERT INTO home_items (root_folder, path, kind, size, mtime, taken_at, taken_from, width, height, duration,
			make, model, lat, lon, has_gps, place, orientation, sum, look, thumb, added_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			it.RootFolder, it.Path, it.Kind, it.Size, it.MTime, it.Taken, it.TakenFrom, it.Width, it.Height, it.Duration,
			it.Make, it.Model, it.Lat, it.Lon, b2i(it.HasGPS), it.Place, it.Orientation, it.Sum, it.Look, it.Thumb, now())
	}
	if err != nil {
		return 0, err
	}
	_, err = s.exec(ctx, `UPDATE home_items SET root_folder = ?, kind = ?, size = ?, mtime = ?, taken_at = ?, taken_from = ?, width = ?, height = ?,
		duration = ?, make = ?, model = ?, lat = ?, lon = ?, has_gps = ?, place = ?, orientation = ?, sum = ?, look = ?, thumb = ? WHERE id = ?`,
		it.RootFolder, it.Kind, it.Size, it.MTime, it.Taken, it.TakenFrom, it.Width, it.Height,
		it.Duration, it.Make, it.Model, it.Lat, it.Lon, b2i(it.HasGPS), it.Place, it.Orientation, it.Sum, it.Look, it.Thumb, id)
	return id, err
}

// SetHomePlace notes the town a photo or video was taken in.
func (s *Store) SetHomePlace(ctx context.Context, id int64, place string) error {
	_, err := s.exec(ctx, "UPDATE home_items SET place = ? WHERE id = ?", place, id)
	return err
}

// DeleteHomeItems forgets photos and videos, such as ones gone from disk
// or moved into a .recycle folder.
func (s *Store) DeleteHomeItems(ctx context.Context, ids []int64) error {
	for len(ids) > 0 {
		n := min(len(ids), 500)
		args := make([]any, n)
		for i, id := range ids[:n] {
			args[i] = id
		}
		if _, err := s.exec(ctx, "DELETE FROM home_items WHERE id IN ("+placeholders(n)+")", args...); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
}

// HomeQuery picks photos and videos for the timeline or an album.
type HomeQuery struct {
	Kind    string // photo, video, or "" for both
	AlbumID int64
	// Before carries on after the last one shown: taken before BeforeTaken,
	// or then with a lower id.
	BeforeTaken int64
	BeforeID    int64
	Limit       int
}

// HomeTimeline lists photos and videos, newest first.
func (s *Store) HomeTimeline(ctx context.Context, q HomeQuery) ([]HomeItem, error) {
	var where []string
	var args []any
	if q.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, q.Kind)
	}
	if q.AlbumID > 0 {
		where = append(where, "id IN (SELECT item_id FROM home_album_items WHERE album_id = ?)")
		args = append(args, q.AlbumID)
	}
	if q.BeforeID > 0 {
		where = append(where, "(taken_at < ? OR (taken_at = ? AND id < ?))")
		args = append(args, q.BeforeTaken, q.BeforeTaken, q.BeforeID)
	}
	w := ""
	if len(where) > 0 {
		w = "WHERE " + strings.Join(where, " AND ") + " "
	}
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	args = append(args, limit)
	return s.homeItems(ctx, w+"ORDER BY taken_at DESC, id DESC LIMIT ?", args...)
}

// HomeTotals counts what's in the home media library.
type HomeTotals struct {
	Photos int   `json:"photos"`
	Videos int   `json:"videos"`
	Size   int64 `json:"size"`
}

// HomeTotals counts the photos and videos.
func (s *Store) HomeTotals(ctx context.Context) (HomeTotals, error) {
	var t HomeTotals
	err := s.row(ctx, `SELECT COALESCE(SUM(CASE WHEN kind = 'photo' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN kind = 'video' THEN 1 ELSE 0 END), 0), COALESCE(SUM(size), 0) FROM home_items`).Scan(&t.Photos, &t.Videos, &t.Size)
	return t, err
}

// HomeTaken lists when each photo or video was taken, for counting them
// by month.
func (s *Store) HomeTaken(ctx context.Context, kind string, albumID int64) ([]int64, error) {
	var where []string
	var args []any
	if kind != "" {
		where = append(where, "kind = ?")
		args = append(args, kind)
	}
	if albumID > 0 {
		where = append(where, "id IN (SELECT item_id FROM home_album_items WHERE album_id = ?)")
		args = append(args, albumID)
	}
	q := "SELECT taken_at FROM home_items"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := s.query(ctx, q+" ORDER BY taken_at DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var t int64
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// HomeAlbum is an album of photos and videos.
type HomeAlbum struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"` // event or own
	Name      string    `json:"name"`
	Place     string    `json:"place,omitempty"`
	Start     int64     `json:"start"`
	End       int64     `json:"end"`
	CoverID   int64     `json:"coverId"`
	Hidden    bool      `json:"hidden,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	Count     int       `json:"count"`
}

const homeAlbumCols = `a.id, a.kind, a.name, a.place, a.start_at, a.end_at,
	COALESCE((SELECT i.id FROM home_album_items ai JOIN home_items i ON i.id = ai.item_id
		WHERE ai.album_id = a.id AND i.id = a.cover_id), 0),
	a.hidden, a.created_at,
	(SELECT COUNT(*) FROM home_album_items ai WHERE ai.album_id = a.id),
	COALESCE((SELECT MIN(i.taken_at) FROM home_album_items ai JOIN home_items i ON i.id = ai.item_id WHERE ai.album_id = a.id), 0),
	COALESCE((SELECT MAX(i.taken_at) FROM home_album_items ai JOIN home_items i ON i.id = ai.item_id WHERE ai.album_id = a.id), 0),
	COALESCE((SELECT i.id FROM home_album_items ai JOIN home_items i ON i.id = ai.item_id
		WHERE ai.album_id = a.id ORDER BY CASE WHEN i.kind = 'photo' THEN 0 ELSE 1 END, i.taken_at, i.id LIMIT 1), 0)`

func scanHomeAlbum(r interface{ Scan(...any) error }) (HomeAlbum, error) {
	var a HomeAlbum
	var hidden int
	var created, first, last, firstID int64
	err := r.Scan(&a.ID, &a.Kind, &a.Name, &a.Place, &a.Start, &a.End, &a.CoverID, &hidden, &created, &a.Count, &first, &last, &firstID)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.Hidden = hidden == 1
	a.CreatedAt = time.Unix(created, 0).UTC()
	// An album's dates and cover follow what's in it, unless a cover was
	// picked that's still there.
	if a.Count > 0 {
		a.Start, a.End = first, last
	}
	if a.CoverID == 0 {
		a.CoverID = firstID
	}
	return a, nil
}

// HomeAlbums lists the albums, newest first: hidden ones too when asked.
func (s *Store) HomeAlbums(ctx context.Context, hidden bool) ([]HomeAlbum, error) {
	w := "WHERE a.hidden = 0 "
	if hidden {
		w = ""
	}
	rows, err := s.query(ctx, "SELECT "+homeAlbumCols+" FROM home_albums a "+w+"ORDER BY a.start_at DESC, a.id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HomeAlbum{}
	for rows.Next() {
		a, err := scanHomeAlbum(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// HomeAlbum finds one album.
func (s *Store) HomeAlbum(ctx context.Context, id int64) (HomeAlbum, error) {
	return scanHomeAlbum(s.row(ctx, "SELECT "+homeAlbumCols+" FROM home_albums a WHERE a.id = ?", id))
}

// HomeAlbumsOf lists the albums a photo or video is in.
func (s *Store) HomeAlbumsOf(ctx context.Context, itemID int64) ([]HomeAlbum, error) {
	rows, err := s.query(ctx, "SELECT "+homeAlbumCols+" FROM home_albums a WHERE a.id IN (SELECT album_id FROM home_album_items WHERE item_id = ?) ORDER BY a.start_at DESC", itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HomeAlbum{}
	for rows.Next() {
		a, err := scanHomeAlbum(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateHomeAlbum makes an album of one's own.
func (s *Store) CreateHomeAlbum(ctx context.Context, name string) (HomeAlbum, error) {
	t := now()
	id, err := s.insert(ctx, "INSERT INTO home_albums (kind, name, start_at, end_at, created_at) VALUES ('own', ?, ?, ?, ?)", name, t, t, t)
	if err != nil {
		return HomeAlbum{}, err
	}
	return s.HomeAlbum(ctx, id)
}

// HomeAlbumEdit changes an album; nil leaves a field as it is.
type HomeAlbumEdit struct {
	Name    *string `json:"name"`
	CoverID *int64  `json:"coverId"`
	Hidden  *bool   `json:"hidden"`
}

// EditHomeAlbum changes an album's name, cover or whether it's put away.
func (s *Store) EditHomeAlbum(ctx context.Context, id int64, e HomeAlbumEdit) error {
	if _, err := s.HomeAlbum(ctx, id); err != nil {
		return err
	}
	if e.Name != nil {
		if _, err := s.exec(ctx, "UPDATE home_albums SET name = ? WHERE id = ?", strings.TrimSpace(*e.Name), id); err != nil {
			return err
		}
	}
	if e.CoverID != nil {
		if _, err := s.exec(ctx, "UPDATE home_albums SET cover_id = ? WHERE id = ?", *e.CoverID, id); err != nil {
			return err
		}
	}
	if e.Hidden != nil {
		if _, err := s.exec(ctx, "UPDATE home_albums SET hidden = ? WHERE id = ?", b2i(*e.Hidden), id); err != nil {
			return err
		}
	}
	return nil
}

// DeleteHomeAlbum deletes an album. Its photos and videos stay.
func (s *Store) DeleteHomeAlbum(ctx context.Context, id int64) error {
	if _, err := s.exec(ctx, "DELETE FROM home_album_items WHERE album_id = ?", id); err != nil {
		return err
	}
	res, err := s.exec(ctx, "DELETE FROM home_albums WHERE id = ?", id)
	if err != nil {
		return err
	}
	return affected(res)
}

// AddToHomeAlbum puts photos and videos in an album; ones in it already
// stay as they are.
func (s *Store) AddToHomeAlbum(ctx context.Context, albumID int64, items []int64) error {
	if _, err := s.HomeAlbum(ctx, albumID); err != nil {
		return err
	}
	t := now()
	for _, id := range items {
		if _, err := s.exec(ctx, `INSERT INTO home_album_items (album_id, item_id, added_at)
			SELECT ?, id, ? FROM home_items WHERE id = ? ON CONFLICT (album_id, item_id) DO NOTHING`, albumID, t, id); err != nil {
			return err
		}
	}
	return nil
}

// RemoveFromHomeAlbum takes photos and videos out of an album. The files
// stay.
func (s *Store) RemoveFromHomeAlbum(ctx context.Context, albumID int64, items []int64) error {
	for _, id := range items {
		if _, err := s.exec(ctx, "DELETE FROM home_album_items WHERE album_id = ? AND item_id = ?", albumID, id); err != nil {
			return err
		}
	}
	return nil
}

// HomeEvent is a run of photos and videos taken together, found by Box
// Office, to keep as an event album.
type HomeEvent struct {
	Items []int64
	Place string
	Start int64
	End   int64
}

// SyncHomeEvents makes the event albums match the events found. An event
// found again keeps its album (the one sharing most of its photos), with
// any name, cover or put-away it was given; one no longer found goes, and
// a new one gets a new album. Albums of one's own aren't touched.
func (s *Store) SyncHomeEvents(ctx context.Context, events []HomeEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT ai.album_id, ai.item_id FROM home_album_items ai JOIN home_albums a ON a.id = ai.album_id WHERE a.kind = 'event'")
	if err != nil {
		return err
	}
	albumOf := map[int64]int64{}
	for rows.Next() {
		var a, i int64
		if err := rows.Scan(&a, &i); err != nil {
			rows.Close()
			return err
		}
		albumOf[i] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var existing []int64
	rows, err = tx.QueryContext(ctx, "SELECT id FROM home_albums WHERE kind = 'event'")
	if err != nil {
		return err
	}
	for rows.Next() {
		var a int64
		if err := rows.Scan(&a); err != nil {
			rows.Close()
			return err
		}
		existing = append(existing, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	used := map[int64]bool{}
	t := now()
	for _, ev := range events {
		votes := map[int64]int{}
		for _, i := range ev.Items {
			if a, ok := albumOf[i]; ok && !used[a] {
				votes[a]++
			}
		}
		var album int64
		best := 0
		for a, n := range votes {
			if n > best || (n == best && a < album) {
				album, best = a, n
			}
		}
		if album > 0 {
			used[album] = true
			if _, err := tx.ExecContext(ctx, s.q("UPDATE home_albums SET place = ?, start_at = ?, end_at = ? WHERE id = ?"), ev.Place, ev.Start, ev.End, album); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, s.q("DELETE FROM home_album_items WHERE album_id = ?"), album); err != nil {
				return err
			}
		} else {
			q := s.q("INSERT INTO home_albums (kind, place, start_at, end_at, created_at) VALUES ('event', ?, ?, ?, ?)")
			if s.postgres {
				if err := tx.QueryRowContext(ctx, q+" RETURNING id", ev.Place, ev.Start, ev.End, t).Scan(&album); err != nil {
					return err
				}
			} else {
				res, err := tx.ExecContext(ctx, q, ev.Place, ev.Start, ev.End, t)
				if err != nil {
					return err
				}
				if album, err = res.LastInsertId(); err != nil {
					return err
				}
			}
			used[album] = true
		}
		for _, i := range ev.Items {
			if _, err := tx.ExecContext(ctx, s.q("INSERT INTO home_album_items (album_id, item_id, added_at) VALUES (?, ?, ?) ON CONFLICT (album_id, item_id) DO NOTHING"), album, i, t); err != nil {
				return err
			}
		}
	}
	for _, a := range existing {
		if used[a] {
			continue
		}
		if _, err := tx.ExecContext(ctx, s.q("DELETE FROM home_album_items WHERE album_id = ?"), a); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.q("DELETE FROM home_albums WHERE id = ?"), a); err != nil {
			return err
		}
	}
	return tx.Commit()
}
