package roster

import (
	"bytes"
	"encoding/json"
	"strings"
)

// rlBotPrimaryID mirrors types.rlBotPrimaryID, re-declared here so the
// fast-path bytes.Contains check stays a no-import check.
const rlBotPrimaryID = "Unknown|0|0"

// RewriteUpdateStateBotIds returns a copy of the raw envelope with
// every Players[].PrimaryId rewritten via types.CanonicalizeBotID.
// Called from the RL-client dispatcher before bus publish so every
// downstream consumer sees one canonical id per bot.
//
// Fast path: if the bytes don't contain the Unknown|0|0 sentinel,
// return the input unchanged.
//
// Splice strategy: decode only the inner Data payload, mutate, splice
// the re-encoded inner string back into the original raw bytes in
// place of the original Data value. Re-marshaling the outer envelope
// would reorder keys alphabetically (Data before Event), and
// extractEventName scans only the first 96 bytes of the wire — a
// multi-KB Data buried before Event would parse as eventName="" and
// drop the packet for every filtered subscriber.
//
// On any decode failure we return the input unchanged. Tracker also
// calls types.CanonicalizeBotID defensively on its own decoded copy,
// so a miss here only affects raw-bus subscribers, and only for that
// one packet.
func RewriteUpdateStateBotIds(raw []byte) []byte {
	if !bytes.Contains(raw, []byte(rlBotPrimaryID)) {
		return raw
	}

	dataStart, dataEnd, dataKey := findDataValueSpan(raw)
	if dataKey == "" {
		return raw
	}
	var innerStr string
	if err := json.Unmarshal(raw[dataStart:dataEnd], &innerStr); err != nil {
		return raw
	}

	var inner map[string]any
	if err := json.Unmarshal([]byte(innerStr), &inner); err != nil {
		return raw
	}
	var playersKey string
	switch {
	case inner["Players"] != nil:
		playersKey = "Players"
	case inner["players"] != nil:
		playersKey = "players"
	default:
		return raw
	}
	playersAny, ok := inner[playersKey].([]any)
	if !ok {
		return raw
	}
	idKey := "PrimaryId"
	nameKey := "Name"
	if playersKey == "players" {
		idKey = "primaryid"
		nameKey = "name"
	}
	rewrote := false
	for _, pAny := range playersAny {
		p, ok := pAny.(map[string]any)
		if !ok {
			continue
		}
		pid, _ := p[idKey].(string)
		if pid != rlBotPrimaryID {
			continue
		}
		name, _ := p[nameKey].(string)
		if name == "" {
			continue
		}
		p[idKey] = "Bot|" + strings.ReplaceAll(name, "|", "_")
		rewrote = true
	}
	if !rewrote {
		return raw
	}

	innerOut, err := json.Marshal(inner)
	if err != nil {
		return raw
	}
	newDataValue, err := json.Marshal(string(innerOut))
	if err != nil {
		return raw
	}

	out := make([]byte, 0, len(raw)+len(newDataValue)-(dataEnd-dataStart))
	out = append(out, raw[:dataStart]...)
	out = append(out, newDataValue...)
	out = append(out, raw[dataEnd:]...)
	return out
}

// findDataValueSpan locates the JSON-string value of the top-level
// "Data" (or "data") field in an RL envelope and returns its byte span
// in raw, plus which casing was found. The returned span includes the
// surrounding quotes so json.Unmarshal can decode it as a string.
//
// Returns key="" when the field isn't present or the envelope can't be
// parsed at the top level. Uses encoding/json's tokenizer to stay
// robust across whitespace, escape sequences, and any extra top-level
// fields RL might add in future builds.
func findDataValueSpan(raw []byte) (start, end int, key string) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return 0, 0, ""
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return 0, 0, ""
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return 0, 0, ""
		}
		k, _ := keyTok.(string)
		if k == "Data" || k == "data" {
			before := int(dec.InputOffset())
			var rm json.RawMessage
			if err := dec.Decode(&rm); err != nil {
				return 0, 0, ""
			}
			after := int(dec.InputOffset())
			s := before
			for s < after && (raw[s] == ' ' || raw[s] == '\t' || raw[s] == '\n' || raw[s] == '\r') {
				s++
			}
			if s >= after || raw[s] != ':' {
				return 0, 0, ""
			}
			s++
			for s < after && (raw[s] == ' ' || raw[s] == '\t' || raw[s] == '\n' || raw[s] == '\r') {
				s++
			}
			if s >= after || raw[s] != '"' {
				return 0, 0, ""
			}
			return s, after, k
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return 0, 0, ""
		}
	}
	return 0, 0, ""
}

