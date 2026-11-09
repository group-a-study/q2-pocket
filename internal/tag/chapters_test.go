package tag

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func chap(h *Head, id string, start, end uint32, title string) []byte {
	b := append([]byte(id), 0)
	t := make([]byte, 16)
	binary.BigEndian.PutUint32(t, start)
	binary.BigEndian.PutUint32(t[4:], end)
	binary.BigEndian.PutUint32(t[8:], 1234)
	binary.BigEndian.PutUint32(t[12:], 5678)
	b = append(b, t...)
	b = append(b, h.frame("TIT2", h.text(title))...)
	return h.frame("CHAP", b)
}

func TestRetime(t *testing.T) {
	h := &Head{version: 4, has: map[string]bool{}}
	toc := append([]byte("toc\x00"), 3, 3)
	toc = append(toc, "c1\x00c2\x00c3\x00"...)
	h.frames = [][]byte{h.frame("TLEN", h.text("100000")), h.frame("CTOC", toc),
		chap(h, "c1", 0, 60000, "Intro"), chap(h, "c2", 60000, 90000, "Sponsor"), chap(h, "c3", 90000, 300000, "Talk")}
	// 60 s to 90 s cut out
	h.Retime(func(ms uint32) uint32 {
		switch {
		case ms <= 60000:
			return ms
		case ms <= 90000:
			return 60000
		}
		return ms - 30000
	})
	var ids []string
	for _, fr := range h.frames {
		ids = append(ids, string(fr[:4]))
		if string(fr[:4]) == "CHAP" && bytes.HasPrefix(fr[10:], []byte("c3\x00")) {
			if s, e := binary.BigEndian.Uint32(fr[13:]), binary.BigEndian.Uint32(fr[17:]); s != 60000 || e != 270000 {
				t.Errorf("c3 at %d-%d", s, e)
			}
			if binary.BigEndian.Uint32(fr[21:]) != 0xffffffff {
				t.Error("byte offset kept")
			}
		}
		if string(fr[:4]) == "CTOC" && !bytes.Contains(fr, []byte("\x02c1\x00c3\x00")) {
			t.Errorf("toc %q", fr[10:])
		}
	}
	if len(ids) != 3 || ids[0] != "CTOC" || ids[1] != "CHAP" || ids[2] != "CHAP" {
		t.Errorf("frames %v", ids)
	}
}

func TestChapters(t *testing.T) {
	h := &Head{version: 3, has: map[string]bool{}}
	h.frames = [][]byte{chap(h, "c1", 0, 60000, "Intro"), chap(h, "c2", 60000, 90000, "Sponsor: Ünïcode")}
	c := h.Chapters()
	if len(c) != 2 || c[1].Start != 60000 || c[1].End != 90000 || c[1].Title != "Sponsor: Ünïcode" || c[0].Title != "Intro" {
		t.Errorf("%+v", c)
	}
}

func TestAddChapters(t *testing.T) {
	h := &Head{version: 3, has: map[string]bool{}}
	if !h.AddChapters([]Chapter{{0, 0, "Intro"}, {65000, 0, "Sponsor: Acme"}, {130000, 0, "The interview"}}, 900000) {
		t.Fatal("not added")
	}
	c := h.Chapters()
	if len(c) != 3 || c[0].End != 65000 || c[2].Start != 130000 || c[2].End != 900000 || c[1].Title != "Sponsor: Acme" {
		t.Errorf("%+v", c)
	}
	if h.AddChapters([]Chapter{{0, 0, "a"}, {1, 0, "b"}}, 2) {
		t.Error("added over chapters it had")
	}
}
