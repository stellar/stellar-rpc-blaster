package benchmark

import (
	"strings"

	"github.com/stellar/go-stellar-sdk/xdr"
)

const (
	maxDiagnosticEventSummaries = 4
	maxDiagnosticTokensPerEvent = 6
)

const (
	diagnosticPriorityError = iota
	diagnosticPriorityOther
	diagnosticPriorityCall
)

func summarizeDiagnosticEvents(events []string) string {
	var grouped [3][]string
	seen := make(map[string]struct{}, len(events))
	for _, encoded := range events {
		summary, priority := summarizeDiagnosticEventWithPriority(encoded)
		if summary == "" {
			continue
		}
		if _, ok := seen[summary]; ok {
			continue
		}
		seen[summary] = struct{}{}
		grouped[priority] = append(grouped[priority], summary)
	}

	parts := make([]string, 0, maxDiagnosticEventSummaries)
	for _, summaries := range grouped {
		for _, summary := range summaries {
			parts = append(parts, summary)
			if len(parts) == maxDiagnosticEventSummaries {
				return strings.Join(parts, " ; ")
			}
		}
	}
	return strings.Join(parts, " ; ")
}

func summarizeDiagnosticEvent(encoded string) string {
	summary, _ := summarizeDiagnosticEventWithPriority(encoded)
	return summary
}

func summarizeDiagnosticEventWithPriority(encoded string) (string, int) {
	var event xdr.DiagnosticEvent
	if err := xdr.SafeUnmarshalBase64(encoded, &event); err != nil {
		return "", diagnosticPriorityOther
	}

	body, ok := event.Event.Body.GetV0()
	if !ok {
		return "", diagnosticPriorityOther
	}
	tokens := append(normalizeDiagnosticScVals(body.Topics), normalizeDiagnosticScVal(body.Data)...)
	if len(tokens) == 0 || isCoreMetricsDiagnostic(tokens) {
		return "", diagnosticPriorityOther
	}

	parts := make([]string, 0, maxDiagnosticTokensPerEvent)
	seen := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		switch token {
		case "", "error", "core_metrics":
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		parts = append(parts, token)
		if len(parts) == maxDiagnosticTokensPerEvent {
			break
		}
	}
	if len(parts) == 0 {
		return "", diagnosticPriorityOther
	}
	return strings.Join(parts, " | "), diagnosticPriority(tokens)
}

func diagnosticPriority(tokens []string) int {
	priority := diagnosticPriorityOther
	for _, token := range tokens {
		if token == "fn_call" {
			priority = diagnosticPriorityCall
		}
		lower := strings.ToLower(token)
		if token == "error" || strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
			return diagnosticPriorityError
		}
	}
	return priority
}

func normalizeDiagnosticScVals(values []xdr.ScVal) []string {
	tokens := make([]string, 0, len(values))
	for _, value := range values {
		tokens = append(tokens, normalizeDiagnosticScVal(value)...)
	}
	return tokens
}

func normalizeDiagnosticScVal(value xdr.ScVal) []string {
	switch value.Type {
	case xdr.ScValTypeScvString:
		return []string{strings.TrimLeft(strings.TrimSpace(string(*value.Str)), "\x00")}
	case xdr.ScValTypeScvSymbol:
		return []string{strings.TrimLeft(strings.TrimSpace(string(*value.Sym)), "\x00")}
	case xdr.ScValTypeScvError:
		return []string{value.String()}
	case xdr.ScValTypeScvVec:
		if value.Vec == nil || *value.Vec == nil {
			return nil
		}
		return normalizeDiagnosticScVals(**value.Vec)
	case xdr.ScValTypeScvMap:
		if value.Map == nil || *value.Map == nil {
			return nil
		}
		var tokens []string
		for _, entry := range **value.Map {
			tokens = append(tokens, normalizeDiagnosticScVal(entry.Key)...)
			tokens = append(tokens, normalizeDiagnosticScVal(entry.Val)...)
		}
		return tokens
	case xdr.ScValTypeScvAddress,
		xdr.ScValTypeScvBytes,
		xdr.ScValTypeScvU32,
		xdr.ScValTypeScvI32,
		xdr.ScValTypeScvU64,
		xdr.ScValTypeScvI64,
		xdr.ScValTypeScvU128,
		xdr.ScValTypeScvI128,
		xdr.ScValTypeScvU256,
		xdr.ScValTypeScvI256,
		xdr.ScValTypeScvTimepoint,
		xdr.ScValTypeScvDuration,
		xdr.ScValTypeScvBool,
		xdr.ScValTypeScvVoid,
		xdr.ScValTypeScvContractInstance,
		xdr.ScValTypeScvLedgerKeyContractInstance,
		xdr.ScValTypeScvLedgerKeyNonce:
		return nil
	default:
		return nil
	}
}

func isCoreMetricsDiagnostic(tokens []string) bool {
	for _, token := range tokens {
		if token == "core_metrics" {
			return true
		}
	}
	return false
}
