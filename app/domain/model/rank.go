package model

import (
	"strings"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
)

// Rank: clave de orden manual de los issues (estilo LexoRank). Es un string
// que se compara byte a byte (en Postgres, columna con COLLATE "C") y entre dos
// claves siempre existe otra, así reordenar toca una sola fila.
//
// Formato ("fractional indexing", como el paquete fractional-indexing de
// rocicorp): una parte entera de largo variable y una fracción opcional, ambas
// en base 62 (0-9A-Za-z, en orden ASCII).
//   - El primer carácter dice el largo de la parte entera: 'a'..'z' = 2..27
//     caracteres (enteros >= 0), 'A'..'Z' = 27..2 (enteros negativos).
//   - Agregar al final incrementa la parte entera ("a0", "a1", ..., "az",
//     "b00"): las claves crecen un carácter cada vez que se multiplica el
//     número de issues, no uno por issue.
//   - La fracción permite ubicar una clave entre dos enteros consecutivos
//     ("a0" < "a0V" < "a1"); nunca termina en '0'.

const rankDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// rankSmallestInteger es la parte entera más chica representable; no es una
// clave válida por sí sola (no habría lugar antes de ella).
var rankSmallestInteger = "A" + strings.Repeat("0", 26)

// FirstRank es la clave del primer issue de un proyecto.
const FirstRank = "a0"

// RankBetween devuelve una clave estrictamente entre a y b. a vacío = sin
// límite inferior; b vacío = sin límite superior (ambos vacíos = FirstRank).
// Claves mal formadas o a >= b -> ValidationError.
func RankBetween(a, b string) (string, error) {
	if a != "" {
		if err := ValidateRank(a); err != nil {
			return "", err
		}
	}
	if b != "" {
		if err := ValidateRank(b); err != nil {
			return "", err
		}
	}
	if a != "" && b != "" && a >= b {
		return "", rankError("rank bounds out of order", a)
	}

	if a == "" {
		if b == "" {
			return FirstRank, nil
		}
		ib := rankIntegerPart(b)
		fb := b[len(ib):]
		if ib == rankSmallestInteger {
			return ib + rankMidpoint("", fb), nil
		}
		if ib < b {
			return ib, nil
		}
		res, ok := decrementRankInteger(ib)
		if !ok {
			return "", rankError("cannot rank before the smallest key", b)
		}
		return res, nil
	}

	ia := rankIntegerPart(a)
	fa := a[len(ia):]
	if b == "" {
		if i, ok := incrementRankInteger(ia); ok {
			return i, nil
		}
		return ia + rankMidpoint(fa, ""), nil
	}

	ib := rankIntegerPart(b)
	fb := b[len(ib):]
	if ia == ib {
		return ia + rankMidpoint(fa, fb), nil
	}
	i, ok := incrementRankInteger(ia)
	if !ok {
		return "", rankError("cannot rank after the largest key", a)
	}
	if i < b {
		return i, nil
	}
	return ia + rankMidpoint(fa, ""), nil
}

// RankAfter devuelve una clave mayor que last (last vacío = FirstRank). Es la
// que recibe un issue nuevo: al final del orden del proyecto.
func RankAfter(last string) (string, error) {
	return RankBetween(last, "")
}

// rankMidpoint devuelve una fracción entre a y b (b vacío = 1). Precondición
// (la garantizan los llamadores): a < b y ninguna termina en '0'.
func rankMidpoint(a, b string) string {
	zero := rankDigits[0]
	if b != "" {
		// Prefijo común (a se completa con ceros a la derecha).
		n := 0
		for n < len(b) && rankDigitAt(a, n, zero) == b[n] {
			n++
		}
		if n > 0 {
			return b[:n] + rankMidpoint(suffix(a, n), b[n:])
		}
	}
	digitA := 0
	if a != "" {
		digitA = strings.IndexByte(rankDigits, a[0])
	}
	digitB := len(rankDigits)
	if b != "" {
		digitB = strings.IndexByte(rankDigits, b[0])
	}
	if digitB-digitA > 1 {
		return string(rankDigits[(digitA+digitB+1)/2])
	}
	if len(b) > 1 {
		return b[:1]
	}
	return string(rankDigits[digitA]) + rankMidpoint(suffix(a, 1), "")
}

func rankDigitAt(s string, i int, fallback byte) byte {
	if i < len(s) {
		return s[i]
	}
	return fallback
}

func suffix(s string, n int) string {
	if n >= len(s) {
		return ""
	}
	return s[n:]
}

// rankIntegerLength devuelve el largo de la parte entera según su primer
// carácter (0 si no es un encabezado válido).
func rankIntegerLength(head byte) int {
	switch {
	case head >= 'a' && head <= 'z':
		return int(head-'a') + 2
	case head >= 'A' && head <= 'Z':
		return int('Z'-head) + 2
	}
	return 0
}

// rankIntegerPart devuelve la parte entera de una clave ya validada.
func rankIntegerPart(key string) string {
	return key[:rankIntegerLength(key[0])]
}

func ValidateRank(key string) error {
	if key == "" || key == rankSmallestInteger {
		return rankError("invalid rank", key)
	}
	n := rankIntegerLength(key[0])
	if n == 0 || n > len(key) {
		return rankError("invalid rank", key)
	}
	for i := 1; i < len(key); i++ {
		if strings.IndexByte(rankDigits, key[i]) < 0 {
			return rankError("invalid rank", key)
		}
	}
	if len(key) > n && key[len(key)-1] == rankDigits[0] {
		return rankError("invalid rank", key)
	}
	return nil
}

// incrementRankInteger suma 1 a una parte entera; ok = false si ya es la más
// grande representable.
func incrementRankInteger(x string) (string, bool) {
	head, digs := x[0], []byte(x[1:])
	carry := true
	for i := len(digs) - 1; carry && i >= 0; i-- {
		d := strings.IndexByte(rankDigits, digs[i]) + 1
		if d == len(rankDigits) {
			digs[i] = rankDigits[0]
		} else {
			digs[i] = rankDigits[d]
			carry = false
		}
	}
	if !carry {
		return string(head) + string(digs), true
	}
	switch head {
	case 'Z':
		return "a" + string(rankDigits[0]), true
	case 'z':
		return "", false
	}
	h := head + 1
	if h > 'a' {
		digs = append(digs, rankDigits[0])
	} else {
		digs = digs[:len(digs)-1]
	}
	return string(h) + string(digs), true
}

// decrementRankInteger resta 1 a una parte entera; ok = false si ya es la más
// chica representable.
func decrementRankInteger(x string) (string, bool) {
	last := rankDigits[len(rankDigits)-1]
	head, digs := x[0], []byte(x[1:])
	borrow := true
	for i := len(digs) - 1; borrow && i >= 0; i-- {
		d := strings.IndexByte(rankDigits, digs[i]) - 1
		if d == -1 {
			digs[i] = last
		} else {
			digs[i] = rankDigits[d]
			borrow = false
		}
	}
	if !borrow {
		return string(head) + string(digs), true
	}
	switch head {
	case 'a':
		return "Z" + string(last), true
	case 'A':
		return "", false
	}
	h := head - 1
	if h < 'Z' {
		digs = append(digs, last)
	} else {
		digs = digs[:len(digs)-1]
	}
	return string(h) + string(digs), true
}

func rankError(msg, key string) error {
	return domainerrors.ValidationError(msg, map[string]any{"field": "rank", "value": key})
}
