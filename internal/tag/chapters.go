package tag

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Retime moves the tag's chapters (CHAP frames) to where m puts their times, in milliseconds, after
// parts of the audio are cut: a chapter left with no length (all of it cut) goes, from the table of
// contents (CTOC) too. Byte offsets no longer hold and are marked unused; the length frame (TLEN)
// goes, the audio's length having changed.
func (h *Head) Retime(m func(ms uint32) uint32) {
	gone := map[string]bool{}
	var keep [][]byte
	for _, fr := range h.frames {
		switch string(fr[:4]) {
		case "TLEN":
			continue
		case "CHAP":
			body := fr[10:]
			i := bytes.IndexByte(body, 0)
			if i < 0 || len(body) < i+17 {
				break
			}
			t := body[i+1:]
			start, end := m(binary.BigEndian.Uint32(t)), m(binary.BigEndian.Uint32(t[4:]))
			if end <= start {
				gone[string(body[:i])] = true
				continue
			}
			fr = append([]byte(nil), fr...)
			t = fr[10+i+1:]
			binary.BigEndian.PutUint32(t, start)
			binary.BigEndian.PutUint32(t[4:], end)
			binary.BigEndian.PutUint32(t[8:], 0xffffffff)
			binary.BigEndian.PutUint32(t[12:], 0xffffffff)
		}
		keep = append(keep, fr)
	}
	for k, fr := range keep {
		if string(fr[:4]) == "CTOC" && len(gone) > 0 {
			keep[k] = h.untoc(fr, gone)
		}
	}
	h.frames = keep
}

// untoc is a CTOC frame without the children named in gone.
func (h *Head) untoc(fr []byte, gone map[string]bool) []byte {
	body := fr[10:]
	i := bytes.IndexByte(body, 0)
	if i < 0 || len(body) < i+3 {
		return fr
	}
	out := append([]byte(nil), body[:i+2]...) // its id, its flags
	n := int(body[i+2])
	p := i + 3
	var kids []byte
	kept := 0
	for k := 0; k < n && p < len(body); k++ {
		e := bytes.IndexByte(body[p:], 0)
		if e < 0 {
			return fr
		}
		if id := string(body[p : p+e]); !gone[id] {
			kids = append(kids, body[p:p+e+1]...)
			kept++
		}
		p += e + 1
	}
	out = append(out, byte(kept))
	out = append(out, kids...)
	out = append(out, body[p:]...) // its own frames (a title)
	return h.frame("CTOC", out)
}

// Chapter is a CHAP frame's start, end (milliseconds) and title.
type Chapter struct {
	Start, End uint32
	Title      string
}

// Chapters is the tag's chapters, in its order.
func (h *Head) Chapters() []Chapter {
	var out []Chapter
	for _, fr := range h.frames {
		if string(fr[:4]) != "CHAP" {
			continue
		}
		body := fr[10:]
		i := bytes.IndexByte(body, 0)
		if i < 0 || len(body) < i+17 {
			continue
		}
		c := Chapter{Start: binary.BigEndian.Uint32(body[i+1:]), End: binary.BigEndian.Uint32(body[i+5:])}
		for sub := body[i+17:]; len(sub) >= 10; {
			n := int(binary.BigEndian.Uint32(sub[4:8]))
			if h.version == 4 {
				n = int(syncsafe(sub[4:8]))
			}
			if n < 0 || n > len(sub)-10 {
				break
			}
			if string(sub[:4]) == "TIT2" {
				c.Title = textOf(sub[10 : 10+n])
			}
			sub = sub[10+n:]
		}
		out = append(out, c)
	}
	return out
}

// textOf is a text frame's body as a string: Latin-1, UTF-16 (with or without a byte order mark)
// or UTF-8.
func textOf(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	enc, b := b[0], b[1:]
	switch enc {
	case 1, 2:
		le := enc == 1 && len(b) >= 2 && b[0] == 0xff && b[1] == 0xfe
		if enc == 1 && len(b) >= 2 && (b[0] == 0xff || b[0] == 0xfe) {
			b = b[2:]
		}
		var r []rune
		for k := 0; k+1 < len(b); k += 2 {
			u := uint16(b[k])<<8 | uint16(b[k+1])
			if le {
				u = uint16(b[k+1])<<8 | uint16(b[k])
			}
			if u == 0 {
				break
			}
			r = append(r, rune(u))
		}
		return string(r)
	case 3:
		return string(bytes.TrimRight(b, "\x00"))
	}
	r := make([]rune, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		r = append(r, rune(c))
	}
	return string(r)
}

// AddChapters gives a tag that has no chapters these, as CHAP frames (each with its title) under a
// table of contents (CTOC); a chapter without an end ends where the next starts, the last at end
// (milliseconds). false when the tag has chapters already, or fewer than two are given.
func (h *Head) AddChapters(chs []Chapter, end uint32) bool {
	if h.has["CHAP"] || len(chs) < 2 {
		return false
	}
	toc := append([]byte("toc\x00"), 0x03, byte(min(len(chs), 255))) // top level, ordered
	for i := range chs[:min(len(chs), 255)] {
		toc = append(toc, fmt.Sprintf("ch%d\x00", i)...)
	}
	h.frames = append(h.frames, h.frame("CTOC", toc))
	for i, c := range chs[:min(len(chs), 255)] {
		to := c.End
		if to <= c.Start {
			to = end
			if i+1 < len(chs) {
				to = chs[i+1].Start
			}
		}
		b := append([]byte(fmt.Sprintf("ch%d", i)), 0)
		t := make([]byte, 16)
		binary.BigEndian.PutUint32(t, c.Start)
		binary.BigEndian.PutUint32(t[4:], to)
		binary.BigEndian.PutUint32(t[8:], 0xffffffff)
		binary.BigEndian.PutUint32(t[12:], 0xffffffff)
		b = append(b, t...)
		if c.Title != "" {
			b = append(b, h.frame("TIT2", h.text(c.Title))...)
		}
		h.frames = append(h.frames, h.frame("CHAP", b))
	}
	h.has["CHAP"], h.has["CTOC"] = true, true
	return true
}
