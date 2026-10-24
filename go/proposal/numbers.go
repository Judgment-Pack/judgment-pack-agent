// Package proposal validates numeric source fidelity without replacing a caller's JSON model.
package proposal

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

const SourceBytes = 4194304
const SourceDepth = 128
const ExponentDigits = 7

type Error struct{ Kind, Detail string }

func (e *Error) Error() string { return e.Detail }
func invalid() error           { return &Error{"invalid-json", "The proposal document is not valid JSON."} }
func bound() error {
	return &Error{"work-bound", "The proposal source exceeds its byte, depth or exponent work bound."}
}
func loss() error {
	return &Error{"number-loss", "The proposal contains a number whose numerical value changes during canonical JSON serialization."}
}

// decimal compares decimal rationals without expanding powers or allocating arbitrary-precision integers.
func decimal(token string) (string, error) {
	negative := strings.HasPrefix(token, "-")
	unsigned := strings.TrimPrefix(token, "-")
	mantissa, exponent := unsigned, "0"
	if at := strings.IndexAny(unsigned, "eE"); at >= 0 {
		mantissa, exponent = unsigned[:at], unsigned[at+1:]
	}
	significant := strings.TrimLeft(strings.TrimLeft(exponent, "+-"), "0")
	// Seven significant exponent digits cover cancellation by every coefficient in 4 MiB.
	if len(significant) > ExponentDigits {
		return "", bound()
	}
	power := 0
	if significant != "" {
		power, _ = strconv.Atoi(significant)
		if strings.HasPrefix(exponent, "-") {
			power = -power
		}
	}
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		power -= len(mantissa) - dot - 1
	}
	digits := strings.TrimLeft(strings.ReplaceAll(mantissa, ".", ""), "0")
	if digits == "" {
		return "0", nil
	}
	stripped := strings.TrimRight(digits, "0")
	power += len(digits) - len(stripped)
	sign := ""
	if negative {
		sign = "-"
	}
	return sign + stripped + "e" + strconv.Itoa(power), nil
}

// Check validates every numeric token in the selected subtree, including overwritten duplicate
// members. Empty path selects the whole source. The caller supplies its existing canonical number
// renderer; no gate, member-order or serializer semantics are changed here.
func Check(data []byte, path []string, numberText func(float64) string) error {
	if len(data) > SourceBytes {
		return bound()
	}
	if !json.Valid(data) {
		return invalid()
	}
	p := scanner{data: data, path: path, numberText: numberText}
	return p.walk(0, 0, len(path) == 0)
}

type scanner struct {
	data       []byte
	at         int
	path       []string
	numberText func(float64) string
}

func (p *scanner) space() {
	for p.at < len(p.data) && strings.ContainsRune(" \t\r\n", rune(p.data[p.at])) {
		p.at++
	}
}
func (p *scanner) text() []byte {
	start := p.at
	p.at++
	for p.at < len(p.data) {
		c := p.data[p.at]
		p.at++
		if c == '\\' {
			p.at++
		} else if c == '"' {
			break
		}
	}
	return p.data[start:p.at]
}
func (p *scanner) walk(depth, matched int, checking bool) error {
	p.space()
	c := p.data[p.at]
	if c == '{' || c == '[' {
		if depth >= SourceDepth {
			return bound()
		}
		end := byte(']')
		if c == '{' {
			end = '}'
		}
		p.at++
		p.space()
		if p.data[p.at] == end {
			p.at++
			return nil
		}
		for {
			next := -1
			if c == '{' {
				raw := p.text()
				var key string
				_ = json.Unmarshal(raw, &key)
				p.space()
				p.at++
				if matched >= 0 && matched < len(p.path) && key == p.path[matched] {
					next = matched + 1
				}
			}
			if err := p.walk(depth+1, next, checking || next == len(p.path)); err != nil {
				return err
			}
			p.space()
			done := p.data[p.at] == end
			p.at++
			if done {
				return nil
			}
			p.space()
		}
	}
	if c == '"' {
		p.text()
		return nil
	}
	start := p.at
	for p.at < len(p.data) && !strings.ContainsRune(" \t\r\n,}]", rune(p.data[p.at])) {
		p.at++
	}
	if checking && (c == '-' || c >= '0' && c <= '9') {
		token := string(p.data[start:p.at])
		source, err := decimal(token)
		if err != nil {
			return err
		}
		number, _ := strconv.ParseFloat(token, 64)
		if math.IsInf(number, 0) || math.IsNaN(number) {
			return loss()
		}
		canonical, err := decimal(p.numberText(number))
		if err != nil {
			return err
		}
		if source != canonical {
			return loss()
		}
	}
	return nil
}
