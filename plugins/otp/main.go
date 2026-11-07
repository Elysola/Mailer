// Plugin otp extracts a one-time verification code from a mail and formats it
// for chat delivery.
//
// Build (TinyGo is required):
//
//	cd plugins/otp && tinygo build -target=wasip1 -buildmode=c-shared \
//	    -o ../otp.wasm -no-debug .
//
// Constraints imposed by running TinyGo WASM as a host-invoked reactor:
//
//   - no regexp / reflect / encoding/json (TinyGo links them against a JS host
//     runtime that wazero does not provide),
//   - tinygo's scheduler is not started, so `defer` and blocking runtime calls
//     must not be used inside exported functions,
//   - memory is reached through raw pointer + unsafe.Slice.
//
// Keep all logic inside the exported functions.
package main

import "unsafe"

const (
	maxInput  = 512 * 1024
	maxOutput = 16 * 1024

	aJSON   = 0
	aText   = 1
	aHTML   = 2
	aSubjct = 3
)

var (
	input   = make([]byte, maxInput)
	output  = make([]byte, maxOutput)
	scratch = make([]byte, maxInput)

	// spans[i] = start of the i-th extracted candidate, spansLen[i] = length.
	spans    [8]int32
	spansLen [8]int32
	nspans   int
)

//export alloc
func alloc(size int32) int32 {
	if size > maxInput || size <= 0 {
		return 0
	}
	return int32(uintptr(unsafe.Pointer(&input[0])))
}

//export process
func process(ptr int32, size int32) int64 {
	_ = ptr
	if size <= 0 || size > maxInput {
		return 0
	}
	body := output[:]
	off := 0

	// Locate the relevant JSON fields without a JSON parser.
	textOff, textLen := findField(input[:size], "text")
	prevOff, prevLen := findField(input[:size], "preview")
	htmlOff, htmlLen := findField(input[:size], "html")
	subjOff, subjLen := findField(input[:size], "subject")

	nspans = 0
	found := scan(input[:size], aText, textOff, textLen)
	if !found {
		found = scan(input[:size], aJSON, prevOff, prevLen)
	}
	if !found {
		found = scan(input[:size], aHTML, htmlOff, htmlLen)
	}
	if !found {
		found = scan(input[:size], aSubjct, subjOff, subjLen)
	}
	if !found || nspans == 0 {
		copy(body, `{"matched":false}`)
		return pack(off + len(`{"matched":false}`))
	}

	start, length := spans[0], spansLen[0]
	code := input[start : start+length]

	off += copy(body[off:], `{"matched":true,"fields":{"code":"`)
	off += copy(body[off:], code)
	off += copy(body[off:], `"},"title":"验证码","text":"🔐 验证码：`)
	off += copy(body[off:], code)
	off += copy(body[off:], `"}`)
	return pack(off)
}

// pack returns the pointer/length pair for the first n bytes of output.
func pack(n int) int64 {
	return int64(uint32(uintptr(unsafe.Pointer(&output[0])))) | int64(n)<<32
}

// findField returns the offset/length of a top-level JSON string field's
// decoded content. Escapes are not decoded (mail bodies rarely escape
// digits), but \n and \" are accounted for by skipping the backslash.
func findField(doc []byte, name string) (int, int) {
	needle := make([]byte, 0, len(name)+3)
	needle = append(needle, '"')
	needle = append(needle, name...)
	needle = append(needle, '"')

	i := 0
	for i+len(needle) <= len(doc) {
		if !matchAt(doc, i, needle) {
			i++
			continue
		}
		j := i + len(needle)
		for j < len(doc) && (doc[j] == ' ' || doc[j] == ':' || doc[j] == '\t') {
			j++
		}
		if j >= len(doc) || doc[j] != '"' {
			i++
			continue
		}
		j++
		start := j
		for j < len(doc) {
			if doc[j] == '\\' {
				j += 2
				continue
			}
			if doc[j] == '"' {
				return start, j - start
			}
			j++
		}
		return start, j - start
	}
	return 0, 0
}

func matchAt(doc []byte, at int, needle []byte) bool {
	if at+len(needle) > len(doc) {
		return false
	}
	for k := 0; k < len(needle); k++ {
		if doc[at+k] != needle[k] {
			return false
		}
	}
	return true
}

// scan looks for verification codes in one field of the JSON document.
func scan(doc []byte, kind int, off, length int) bool {
	if length <= 0 || off < 0 || off+length > len(doc) {
		return false
	}
	field := doc[off : off+length]

	// For HTML, flatten markup into scratch so tags do not split codes.
	if kind == aHTML {
		n := flatten(field)
		field = scratch[:n]
	}

	keywords := keywordsFor(kind)
	for _, kw := range keywords {
		pos := indexFold(field, kw)
		if pos < 0 {
			continue
		}
		// Search for a 6-digit run after the keyword, allowing up to 40
		// non-digit characters (spaces, "is", "：", tags...).
		limit := pos + len(kw) + 40
		if limit > len(field) {
			limit = len(field)
		}
		if start, end, ok := findCode(field, pos+len(kw), limit); ok {
			spans[nspans] = int32(off + start)
			spansLen[nspans] = int32(end - start)
			nspans++
			return true
		}
	}

	// No keyword matched: accept a standalone 6-digit run that is not part of
	// a longer number.
	if start, end, ok := findCode(field, 0, len(field)); ok {
		if looksLikeCode(field, start, end) {
			spans[nspans] = int32(off + start)
			spansLen[nspans] = int32(end - start)
			nspans++
			return true
		}
	}
	return false
}

func keywordsFor(kind int) []string {
	switch kind {
	case aSubjct:
		return []string{"code", "验证码", "校验码"}
	default:
		return []string{
			"verification code", "security code", "one-time code",
			"your code is", "验证码", "校验码", "动态码", "验证身份",
		}
	}
}

// findCode returns the first maximal run of exactly six digits in
// field[from:to].
func findCode(field []byte, from, to int) (int, int, bool) {
	i := from
	for i < to {
		if !isDigit(field[i]) {
			i++
			continue
		}
		j := i
		for j < to && isDigit(field[j]) {
			j++
		}
		if j-i == 6 {
			return i, j, true
		}
		i = j
	}
	return 0, 0, false
}

// looksLikeCode rejects runs that are part of a longer number or a date-like
// token, when scanning the whole field with no keyword context.
func looksLikeCode(field []byte, start, end int) bool {
	if start > 0 && (isDigit(field[start-1]) || field[start-1] == '-' || field[start-1] == '/') {
		return false
	}
	if end < len(field) && (isDigit(field[end]) || field[end] == '-' || field[end] == '/') {
		return false
	}
	return true
}

// flatten copies an HTML fragment into scratch with tags replaced by spaces.
func flatten(src []byte) int {
	n := 0
	inTag := false
	for _, b := range src {
		switch {
		case b == '<':
			inTag = true
			if n < len(scratch) {
				scratch[n] = ' '
				n++
			}
		case b == '>':
			inTag = false
		case !inTag:
			if n < len(scratch) {
				scratch[n] = b
				n++
			}
		}
	}
	return n
}

func indexFold(haystack []byte, needle string) int {
	n := len(needle)
	if n == 0 || len(haystack) < n {
		return -1
	}
	for i := 0; i+n <= len(haystack); i++ {
		ok := true
		for k := 0; k < n; k++ {
			if lower(haystack[i+k]) != lower(needle[k]) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func lower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func main() {}
