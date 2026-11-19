package satellite

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"
)

// FireID derives a stable fire identifier from its anchor detection's key
// (D6): "fire-" followed by the first 16 hex characters of SHA-256(anchor).
// The anchor, once assigned at a fire's creation, never changes for that
// fire's lifetime (see Assign), so FireID is only ever computed once per
// fire -- it is not recomputed from the fire's current membership on every
// batch.
func FireID(anchorKey string) string {
	sum := sha256.Sum256([]byte(anchorKey))
	return "fire-" + hex.EncodeToString(sum[:])[:16]
}

// Anchor is the detection that gave a fire its identity: the member with
// the smallest (detected_at, key) ever known to belong to its component,
// counting a detection that was later retracted just as much as one still
// active (PR #6 review finding, round 2: a fire's identity must not depend
// on whether a transport batch boundary happened to let a since-retracted
// member be evaluated while it was still active).
type Anchor struct {
	Key string
	At  time.Time
}

// FireMeta is the lineage state Assign needs for one existing fire,
// supplied by the caller's durable state (Fase 4) and otherwise opaque to
// this package.
type FireMeta struct {
	Anchor Anchor
	Active bool
}

// anchorOf returns the earliest-(detected_at, key) detection among
// members, matching the ordering Components already establishes but
// computed independently here so Assign does not depend on its caller
// having pre-sorted members. Callers that need a batch-boundary-immune
// anchor must pass an anchor pool that already includes every detection
// -- active or since-retracted -- ever connected to this component, not
// just its currently-active members (see materialize's anchorPools).
func anchorOf(members []Detection) Anchor {
	best := members[0]
	for _, d := range members[1:] {
		if d.At.Before(best.At) || (d.At.Equal(best.At) && d.Key() < best.Key()) {
			best = d
		}
	}
	return Anchor{Key: best.Key(), At: best.At}
}

func earlier(a, b Anchor) bool {
	if !a.At.Equal(b.At) {
		return a.At.Before(b.At)
	}
	return a.Key < b.Key
}

// Assign maps each of this batch's connected components (Components,
// filtered to groups of >=2) to a fire ID, given the previous batch's
// detection-key-to-fire-ID membership (prev) and the metadata of every fire
// currently known (fires, keyed by ID). It implements D6's merge/split
// rules as one pure, stateless-per-call function; Fase 4's recognizer owns
// persisting prev/fires between calls and translating the results into
// upsert/retract Mutations.
//
// anchorPools[i] is the full anchor-candidacy pool for components[i]:
// normally identical to components[i], but the caller (materialize) may
// widen it with detections that are no longer active (e.g. retracted
// within the very same batch that created this component) yet are still
// geographically connected to it -- so a detection that briefly existed
// in the feed still counts toward this fire's identity even though it
// never survives into the component itself (PR #6 review finding, round
// 2). len(anchorPools) must equal len(components); anchorPools[i] must be
// non-empty. Passing components itself as anchorPools reproduces the
// pre-expansion behavior exactly.
//
// ids[i] is the fire ID for components[i], in the same order. retracted
// lists every previously-active fire ID that no longer has a surviving
// component in this batch (a merge's losing side, or a fire that
// disappeared entirely). created maps every brand-new fire ID introduced
// by this call to the anchor that produced it, so the recognizer can record
// FireMeta for it going forward.
func Assign(components [][]Detection, anchorPools [][]Detection, prev map[string]string, fires map[string]FireMeta) (ids []string, retracted []string, created map[string]Anchor) {
	created = make(map[string]Anchor)
	ids = make([]string, len(components))

	// previousIDsOf[i] = the set of distinct fire IDs any member of
	// components[i] belonged to in the previous batch.
	previousIDsOf := make([]map[string]bool, len(components))
	for i, members := range components {
		set := make(map[string]bool)
		for _, d := range members {
			if id, ok := prev[d.Key()]; ok {
				set[id] = true
			}
		}
		previousIDsOf[i] = set
	}

	// splitSource[oldID] = indices of every component that inherited at
	// least one member from oldID. An oldID claimed by more than one
	// component this batch has split.
	splitSource := make(map[string][]int)
	for i, set := range previousIDsOf {
		for id := range set {
			splitSource[id] = append(splitSource[id], i)
		}
	}

	assignedTo := make(map[int]bool) // component index -> already decided
	survivingOldIDs := make(map[string]bool)

	// First pass: resolve every previous ID's fate (merge survivor, split
	// survivor, or untouched continuation), deciding every component that
	// a known previous ID still reaches.
	oldIDsInOrder := make([]string, 0, len(splitSource))
	for id := range splitSource {
		oldIDsInOrder = append(oldIDsInOrder, id)
	}
	sort.Strings(oldIDsInOrder) // deterministic iteration order

	for _, oldID := range oldIDsInOrder {
		touching := splitSource[oldID]
		meta, known := fires[oldID]

		if len(touching) > 1 {
			// Split: oldID's members landed in multiple components.
			// The surviving component is the one containing the
			// anchor detection (if that detection is still an active
			// member anywhere this batch and still inside this old
			// fire's lineage), otherwise the one containing the
			// globally earliest former member of oldID among this
			// batch's surviving members.
			winner := splitWinner(components, anchorPools, touching, meta, known)
			for _, idx := range touching {
				if idx == winner {
					if !assignedTo[idx] || ids[idx] == oldID {
						ids[idx] = oldID
						assignedTo[idx] = true
						survivingOldIDs[oldID] = true
					}
				}
			}
			continue
		}

		// touching has exactly one component; whether oldID survives
		// there depends on whether other old IDs also touch it (a
		// merge) -- resolved below once we know every old ID's single
		// target component.
	}

	// Second pass: for components not yet assigned by a split decision,
	// resolve merges (multiple old IDs -> one component) by earliest
	// anchor, or a plain continuation (exactly one old ID -> one
	// component, and that old ID did not split elsewhere).
	for i := range components {
		if assignedTo[i] {
			continue
		}
		candidates := make([]string, 0, len(previousIDsOf[i]))
		for id := range previousIDsOf[i] {
			// Skip an old ID that this pass already resolved as a
			// split and granted to a *different* component.
			if len(splitSource[id]) > 1 && !survivingOldIDs[id] {
				continue
			}
			if len(splitSource[id]) > 1 && survivingOldIDs[id] {
				// Already decided by the split branch; it must be
				// this very component for the ID to appear here.
				continue
			}
			candidates = append(candidates, id)
		}

		switch len(candidates) {
		case 0:
			anchor := anchorOf(anchorPools[i])
			id := newOrReactivatedID(anchor, fires, assignedTo, ids)
			ids[i] = id
			if _, exists := fires[id]; !exists {
				created[id] = anchor
			}
		case 1:
			ids[i] = reAnchorIfEarlier(anchorPools[i], candidates[0], fires, assignedTo, ids, created, &retracted)
		default:
			// Merge: the surviving ID is whichever has the earliest
			// anchor; every other candidate is retracted.
			sort.Strings(candidates) // deterministic tie input order
			survivor := candidates[0]
			for _, id := range candidates[1:] {
				if meta, ok := fires[id]; ok {
					if sMeta, sok := fires[survivor]; !sok || earlier(meta.Anchor, sMeta.Anchor) {
						survivor = id
					}
				}
			}
			for _, id := range candidates {
				if id != survivor {
					retracted = append(retracted, id)
				}
			}
			ids[i] = reAnchorIfEarlier(anchorPools[i], survivor, fires, assignedTo, ids, created, &retracted)
		}
		assignedTo[i] = true
	}

	// Any previously-active fire whose ID was not assigned to any
	// surviving component this batch, and was not already recorded as
	// retracted by a merge above, has disappeared entirely.
	assignedIDs := make(map[string]bool, len(ids))
	for _, id := range ids {
		assignedIDs[id] = true
	}
	retractedSet := make(map[string]bool, len(retracted))
	for _, id := range retracted {
		retractedSet[id] = true
	}
	touchedOldIDs := make(map[string]bool)
	for _, id := range prev {
		touchedOldIDs[id] = true
	}
	for id, meta := range fires {
		if !meta.Active {
			continue
		}
		if assignedIDs[id] || retractedSet[id] {
			continue
		}
		if touchedOldIDs[id] {
			// Touched but never ended up on a surviving component
			// (every former member moved to a component that was
			// granted a different ID, or shrank below 2 members) --
			// treated as disappeared.
			retracted = append(retracted, id)
		}
	}

	return ids, retracted, created
}

// reAnchorIfEarlier returns the final fire ID for a component that
// continues (or merges into) oldID, after checking whether a
// chronologically earlier member has ever joined its anchor pool since
// oldID's anchor was fixed. FireID is only ever minted once per fire and
// never recomputed from current membership (see FireID's doc comment) --
// but that stability guarantee silently assumed the anchor known at
// creation really was the eventual earliest member. anchorPool breaks
// that assumption in two ways the caller must already have accounted
// for: a detection that arrives late (within the allowed-lateness window)
// but is chronologically earlier than every member known so far (round
// 1), or one that is briefly observed and retracted before it can ever
// become an active component member (round 2, PR #6 review). Without
// this check, the very same final set of detections could mint a
// different ID depending on which batch boundary happened to exist when
// the fire was first created.
//
// When the true anchor (anchorOf(anchorPool)) is earlier than oldID's
// recorded anchor, oldID is retracted and a fresh ID is minted from the
// true anchor instead -- converging, after enough batches, on the same
// final ID a single combined batch would have produced from the start
// (P4.5/P4.8), at the cost of one extra retract+upsert transition when
// the correction happens.
func reAnchorIfEarlier(anchorPool []Detection, oldID string, fires map[string]FireMeta, assignedTo map[int]bool, ids []string, created map[string]Anchor, retracted *[]string) string {
	trueAnchor := anchorOf(anchorPool)
	meta, known := fires[oldID]
	if !known || !earlier(trueAnchor, meta.Anchor) {
		return oldID
	}
	*retracted = append(*retracted, oldID)
	newID := newOrReactivatedID(trueAnchor, fires, assignedTo, ids)
	created[newID] = trueAnchor
	return newID
}

// splitWinner picks which of oldID's candidate components keeps oldID when
// oldID's former membership has split across more than one component this
// batch. anchorPools is index-aligned with components, exactly as in
// Assign.
func splitWinner(components [][]Detection, anchorPools [][]Detection, candidateIdx []int, meta FireMeta, known bool) int {
	if known {
		for _, idx := range candidateIdx {
			for _, d := range components[idx] {
				if d.Key() == meta.Anchor.Key {
					return idx
				}
			}
		}
	}
	// Anchor detection not present (retracted or not in this batch at
	// all): fall back to the component containing the earliest member
	// among the candidates' anchor pools.
	best := candidateIdx[0]
	bestAnchor := anchorOf(anchorPools[best])
	for _, idx := range candidateIdx[1:] {
		a := anchorOf(anchorPools[idx])
		if earlier(a, bestAnchor) {
			best, bestAnchor = idx, a
		}
	}
	return best
}

// newOrReactivatedID picks the ID for a component with no previous
// membership at all. If that anchor's derived ID already exists but its
// fire is inactive (a tombstoned fire whose anchor detection recurs, e.g.
// after a retract-then-reappear), the same ID is reactivated rather than
// minted fresh. If the derived ID already exists and is active -- only
// possible if the very same physical detection becomes two different
// components' anchor in the same batch, which Components's own grouping
// prevents for a single call, but is defended here in case callers compose
// batches across data they did not generate -- a deterministic suffixed ID
// is used instead so two live fires are never silently merged by colliding
// identifiers.
func newOrReactivatedID(anchor Anchor, fires map[string]FireMeta, assignedTo map[int]bool, ids []string) string {
	base := FireID(anchor.Key)
	if meta, exists := fires[base]; !exists || !meta.Active {
		return base
	}
	for n := 1; ; n++ {
		suffixed := FireID(anchor.Key + "#" + itoa(n))
		if _, exists := fires[suffixed]; !exists {
			taken := false
			for _, id := range ids {
				if id == suffixed {
					taken = true
					break
				}
			}
			if !taken {
				return suffixed
			}
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
