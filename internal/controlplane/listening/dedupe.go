package listening

import "slices"

// nearDuplicate is the similarity at which two items of one source are the same post: last30days dedupe_items'
// threshold (lib/dedupe.py:155), last30days-cn's (lib/dedupe.py:169).
const nearDuplicate = 0.7

// Dedupe removes duplicates, keeping the first of each and folding the later copies into it: the same (source,
// native id) (obsei Record.make_id, core/record.py:74-76; harken Mention.compute_id), then the same address across
// sources, a Reddit thread by its thread (last30days fusion.candidate_key, lib/fusion.py:66-79), then near-duplicate
// text within a source, Jaccard at 0.7 or more (last30days dedupe_items, lib/dedupe.py:155-235).
func Dedupe(items []Item) []Item {
	var kept []Item

	byID := map[string]int{}
	byURL := map[string]int{}

	for _, it := range items {
		if i, ok := byID[it.Source+"\x1f"+it.NativeID]; ok {
			kept[i] = merge(kept[i], it)

			continue
		}

		key := identity(it)
		if i, ok := byURL[key]; ok && key != "" {
			kept[i] = merge(kept[i], it)

			continue
		}

		byID[it.Source+"\x1f"+it.NativeID] = len(kept)
		if key != "" {
			byURL[key] = len(kept)
		}

		kept = append(kept, it)
	}

	var (
		out   []Item
		texts []prepared
	)

	for _, it := range kept {
		p := prepare(itemText(it))
		duplicate := false

		for i, o := range out {
			if o.Source == it.Source && len(p.words) > 0 && p.similarity(texts[i]) >= nearDuplicate {
				out[i] = merge(o, it)
				duplicate = true

				break
			}
		}

		if !duplicate {
			out = append(out, it)
			texts = append(texts, p)
		}
	}

	return out
}

// identity is the address duplicates share: a Reddit thread's key or the normalized address.
func identity(it Item) string {
	if it.URL == "" {
		return ""
	}

	return NormalizeURL(it.URL)
}

// itemText is what near-duplicates are compared on: the title, the text, the author and where it was said
// (last30days dedupe.item_text).
func itemText(it Item) string {
	return it.Title + " " + it.Text + " " + it.Author + " " + it.Container
}

// merge folds a later copy into the one kept: the larger of each count, the longer text, what the kept copy lacks,
// the subqueries of both, and the copy's source when another source found it (last30days merge_source_items,
// lib/fusion.py:314-422).
func merge(kept, later Item) Item {
	for _, label := range later.Subqueries {
		if !slices.Contains(kept.Subqueries, label) {
			kept.Subqueries = append(slices.Clone(kept.Subqueries), label)
		}
	}

	for _, src := range append([]string{later.Source}, later.SeenIn...) {
		if src != kept.Source && !slices.Contains(kept.SeenIn, src) {
			kept.SeenIn = append(slices.Clone(kept.SeenIn), src)
		}
	}

	if len(later.Engagement) > 0 {
		eng := make(map[string]float64, len(kept.Engagement)+len(later.Engagement))
		for k, v := range kept.Engagement {
			eng[k] = v
		}

		for k, v := range later.Engagement {
			if cur, ok := eng[k]; !ok || v > cur {
				eng[k] = v
			}
		}

		kept.Engagement = eng
	}

	if len([]rune(later.Text)) > len([]rune(kept.Text)) {
		kept.Text = later.Text
	}

	if kept.Title == "" {
		kept.Title = later.Title
	}

	if kept.Author == "" {
		kept.Author = later.Author
	}

	if kept.Container == "" {
		kept.Container = later.Container
	}

	if kept.PublishedAt == nil && later.PublishedAt != nil {
		kept.PublishedAt, kept.DateConfidence = later.PublishedAt, later.DateConfidence
	}

	return kept
}
