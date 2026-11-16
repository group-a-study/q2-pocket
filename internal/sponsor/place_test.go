package sponsor

import (
	"fmt"
	"math"
	"testing"
)

// talk is a transcript's conversation from a to b (its copy's seconds), a cue every 6 s.
func talk(a, b float64) []Cue {
	var out []Cue
	for t := a; t < b; t += 6 {
		out = append(out, Cue{t, math.Min(t+6, b), fmt.Sprintf("and that is what we talked about at %.0f", t)})
	}
	return out
}

// read is a sponsor read from a, about n seconds, as a transcript has it.
func read(a, n float64, brand string) []Cue {
	return []Cue{
		{a, a + 6, "Today's episode is sponsored by " + brand + ". Now you probably heard me talk about it"},
		{a + 6, a + n - 6, "it makes it really easy and I absolutely love it"},
		{a + n - 6, a + n, "all you have to do is go to " + brand + ".com forward slash live more to get started"},
	}
}

func cat(parts ...[]Cue) []Cue {
	var out []Cue
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// The user's episode (Feel Better, Live More #625): Pocket Casts' copy had two reads, 124 s and
// 120 s, in two slots the user's copy has as 1.5 s of digital silence; the transcript is 242 s
// longer than the download. Placed through the slots: nothing of the reads is in the download, and
// the conversation after each lines up.
func TestPlaceEmptySlots(t *testing.T) {
	cues := cat(talk(0, 1677.5), read(1677.5, 124, "theway"), talk(1801.5, 3621), read(3621, 120, "drinkag1"), talk(3741, 7470))
	slots := []Slot{{From: 1677.35, To: 1678.84}, {From: 3497.4, To: 3498.9}}
	p := Place(cues, slots, 7238)
	if p == nil {
		t.Fatal("not placed")
	}
	if p.Slots != 2 || len(p.Reads) != 0 {
		t.Fatalf("%d slots, reads %+v", p.Slots, p.Reads)
	}
	if _, ok := p.At(1700); ok {
		t.Error("inside the first read: the download lacks it")
	}
	// the conversation resumes at the slot's end (to the second: the next read's start says where)
	if at, ok := p.At(2000); !ok || math.Abs(at-(2000-(1801.5-1678.84))) > 1.5 {
		t.Errorf("after the first read: %v %v", at, ok)
	}
	if at, ok := p.At(5000); !ok || math.Abs(at-(5000-(3741-3498.9))) > 0.5 {
		t.Errorf("after both: %v", at)
	}
	if at, _ := p.At(100); at != 100 {
		t.Errorf("before the slots: %v", at)
	}
}

// A read recorded into the episode, at no slot, is in the download too: placed, it is cut there.
func TestPlaceBakedRead(t *testing.T) {
	cues := cat(talk(0, 1677.5), read(1677.5, 124, "theway"), talk(1801.5, 5000), read(5000, 60, "athletic"), talk(5060, 7470))
	slots := []Slot{{From: 1677.35, To: 1678.84}}
	p := Place(cues, slots, 7470-(1801.5-1678.84)+5)
	if p == nil || len(p.Reads) != 1 {
		t.Fatalf("%+v", p)
	}
	if r := p.Reads[0]; math.Abs(r.From-(5000-(1801.5-1678.84))) > 0.5 || math.Abs(r.To-r.From-60) > 0.5 {
		t.Errorf("the read in the download: %+v", r)
	}
}

// An advert cut from the download that the transcript's copy lacks (its slot empty there), beside a
// read only the transcript's copy has: both accounted for.
func TestPlaceAdTheTranscriptLacks(t *testing.T) {
	// download: talk 0-600, ad 600-630, talk 630-1500, silence 1500-1501.5, talk to 3000.5
	// transcript's copy: talk 0-600, talk 600-1470 (no ad), read 1470-1560, talk 1560-3059
	cues := cat(talk(0, 1470), read(1470, 90, "squarespace"), talk(1560, 3059))
	slots := []Slot{{From: 600, To: 630, Ad: true}, {From: 1500, To: 1501.5}}
	p := Place(cues, slots, 3000.5)
	if p == nil || p.Slots != 1 || len(p.Reads) != 0 {
		t.Fatalf("%+v", p)
	}
	if at, ok := p.At(700); !ok || math.Abs(at-730) > 0.5 {
		t.Errorf("after the ad only the download has: %v", at)
	}
}

// A pre-roll and a post-roll only the transcript's copy has: the episode's start and end are slots.
func TestPlaceRolls(t *testing.T) {
	cues := cat(read(0, 60, "squarespace"), talk(60, 3060), read(3061, 45, "athletic"))
	p := Place(cues, nil, 3001)
	if p == nil || p.Slots != 2 || len(p.Reads) != 0 {
		t.Fatalf("%+v", p)
	}
	if at, ok := p.At(1060); !ok || math.Abs(at-1000) > 0.5 {
		t.Errorf("after the pre-roll: %v", at)
	}
}

// Nothing that adds up: not placed (its reads can't be trusted in the download's seconds).
func TestPlaceNothingFits(t *testing.T) {
	cues := cat(talk(0, 1677.5), read(1677.5, 124, "theway"), talk(1801.5, 7470))
	if Place(cues, []Slot{{From: 1677.35, To: 1678.84}}, 7000) != nil {
		t.Error("lengths that don't add up")
	}
	if Place(cues, []Slot{{From: 900, To: 901.5}}, 7470-123) != nil {
		t.Error("no read at a slot")
	}
	if Place(cues, nil, 7238) != nil {
		t.Error("no slots but the ends, the read in the middle")
	}
}
