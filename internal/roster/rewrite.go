package roster

import (
	"bytes"
	"encoding/json"
	"strings"
)

// rlBotPrimaryID is the literal PrimaryId Rocket League ships for
// every AI player. Mirrors types.rlBotPrimaryID — re-declared here so
// the fast-path bytes.Contains check inside RewriteUpdateStateBotIds
// doesn't need a cross-package call.
const rlBotPrimaryID = "Unknown|0|0"

// RewriteUpdateStateBotIds returns a copy of the raw envelope with every
// Players[].PrimaryId rewritten via types.CanonicalizeBotID. Called from
// the RL-client dispatcher before the bus publish, so every downstream
// consumer — bus subscribers, hosted-bus aggregator, direct-mode
// plugins, the SDK's match.build — sees one canonical id per bot.
//
// Fast path: if the wire bytes don't contain the Unknown|0|0 sentinel,
// return the input unchanged. The vast majority of packets in a
// human-only match hit this branch and skip the decode/re-marshal
// allocation entirely.
//
// Splice strategy: we decode only the inner Data payload, mutate it,
// then splice the re-encoded inner string back into the original raw
// bytes in place of the original Data value. This preserves the outer
// envelope's byte layout — crucially, the position of the "Event" key.
// Going through json.Marshal on the outer envelope reorders keys
// alphabetically (Data before Event); extractEventName scans only the
// first 96 bytes of the wire, so a reordered envelope with a multi-KB
// Data string buried before the Event key would parse as eventName="",
// and Bus.Broadcast would drop the packet for every filtered
// subscriber. Splicing avoids the reorder entirely.
//
// Failure mode: if any decode step fails (malformed envelope,
// unexpected nesting), we return the input unchanged rather than
// dropping the packet. The downstream Tracker also calls
// types.CanonicalizeBotID on its own decoded copy as defense-in-depth,
// so a missed rewrite here only affects raw-bus subscribers (and even
// then, only for that one packet).
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

