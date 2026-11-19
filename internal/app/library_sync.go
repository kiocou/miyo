package app

import (
	"time"

	"netease-music-mygo/internal/migu"
	"netease-music-mygo/internal/netease"
)

const personalRefreshGap = 5 * time.Second

var (
	fetchPlaylist          = netease.PlaylistDetail
	fetchLiked             = netease.LikedIDs
	fetchPersonalPlaylists = netease.UserPlaylists
)

type librarySync struct {
	at     time.Time
	busy   bool
	pollAt time.Time
}

type libraryReply struct {
	liked              []int64
	likedTracks        []netease.Track
	made, saved        []netease.Playlist
	likesErr, listsErr error
}

// freshenPersonal checks changes made on another device without emptying
// any page. Focus and re-entry share a short throttle.
func (a *app) freshenPersonal() {
	a.freshenHome()
	a.refreshLibrary(time.Now())
	if a.route.page == pagePlaylist {
		a.loadPlaylist(a.route.id, false)
	}
}

func (a *app) pollPersonal(now time.Time) {
	if now.Sub(a.librarySync.pollAt) < 30*time.Second {
		return
	}
	a.librarySync.pollAt = now
	a.freshenPersonal()
}

func (a *app) refreshLibrary(now time.Time) {
	if !a.sourceSignedIn() || a.win == nil || a.librarySync.busy || len(a.likeEdits) != 0 || a.likeBatches != 0 || now.Sub(a.librarySync.at) < personalRefreshGap {
		return
	}
	a.librarySync.busy, a.librarySync.at = true, now
	miguSrc := a.isMiguSource()
	uid, gen, revision := a.activeUserID(), a.accountGen, a.likeRevision
	go func() {
		var r libraryReply
		if miguSrc {
			tracks, err := miguLikedTracks()
			r.likesErr = err
			r.likedTracks = tracks
			for _, tr := range tracks {
				r.liked = append(r.liked, tr.ID)
			}
			r.made, r.saved, r.listsErr = miguUserPlaylists()
		} else {
			r.liked, r.likesErr = fetchLiked(uid)
			r.made, r.saved, r.listsErr = fetchPersonalPlaylists(uid)
		}
		a.post(func() { a.libraryAnswered(gen, revision, r) })
	}()
}

func (a *app) libraryAnswered(gen, revision int, r libraryReply) {
	if gen != a.accountGen || !a.sourceSignedIn() {
		return
	}
	a.librarySync.busy = false
	// A heart changed after this request started. Keep that newer state,
	// including its playlist edits, until the next quiet refresh.
	if revision != a.likeRevision || len(a.likeEdits) != 0 || a.likeBatches != 0 {
		return
	}
	if r.likesErr == nil {
		a.rememberMiguTracks(r.likedTracks)
		a.liked = make(map[int64]bool, len(r.liked))
		for _, id := range r.liked {
			a.liked[id] = true
		}
	}
	if r.listsErr == nil {
		a.rememberMiguPlaylists(r.made)
		a.rememberMiguPlaylists(r.saved)
		a.mine, a.saved = r.made, r.saved
		if a.isMiguSource() && len(r.likedTracks) > 0 {
			for i := range a.mine {
				if a.mine[i].SpecialType != 5 {
					continue
				}
				if a.mine[i].TrackCount == 0 {
					a.mine[i].TrackCount = len(r.likedTracks)
				}
				if a.mine[i].CoverURL == "" && r.likedTracks[0].Album.PicURL != "" {
					a.mine[i].CoverURL = r.likedTracks[0].Album.PicURL
					a.mine[i].PicURL = a.mine[i].CoverURL
				}
				if a.playlists[a.mine[i].ID] == nil || len(a.playlists[a.mine[i].ID].Tracks) == 0 {
					pl := a.mine[i]
					pl.Tracks = r.likedTracks
					a.playlists[pl.ID] = &pl
				}
				break
			}
		}
		// Ensure a "我喜欢的音乐" virtual playlist shows in Migu mode when liked songs exist.
		if a.isMiguSource() && len(r.likedTracks) > 0 && a.likedPlaylist() == 0 {
			fav := netease.Playlist{
				ID:          migu.VirtualID("playlist", "migu-liked"),
				Name:        "我喜欢的音乐",
				SpecialType: 5,
				TrackCount:  len(r.likedTracks),
				Tracks:      r.likedTracks,
				Source:      migu.Source,
				SourceID:    "migu-liked",
			}
			if fav.Tracks[0].Album.PicURL != "" {
				fav.CoverURL = fav.Tracks[0].Album.PicURL
				fav.PicURL = fav.CoverURL
			}
			a.mine = append([]netease.Playlist{fav}, a.mine...)
			a.playlists[fav.ID] = &fav
		}
	}
}

func (a *app) isLikedPlaylist(id int64) bool {
	if pl := a.playlists[id]; pl != nil && pl.SpecialType == 5 {
		return true
	}
	for _, pl := range a.mine {
		if pl.ID == id && pl.SpecialType == 5 {
			return true
		}
	}
	return false
}
