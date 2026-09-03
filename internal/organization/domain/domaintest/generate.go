// Package domaintest generates valid Turkish registry identifiers for tests. Values are
// random and checksum-correct; none is a real tax or identity number.
package domaintest

import (
	"math/rand/v2"
	"strconv"
	"strings"
)

// GenerateTCKN builds a valid national identity number from nine random digits.
func GenerateTCKN(r *rand.Rand) string {
	d := make([]int, 11)
	d[0] = 1 + r.IntN(9)
	for i := 1; i < 9; i++ {
		d[i] = r.IntN(10)
	}
	odd := d[0] + d[2] + d[4] + d[6] + d[8]
	even := d[1] + d[3] + d[5] + d[7]
	d[9] = ((odd*7-even)%10 + 10) % 10
	sum := 0
	for i := 0; i < 10; i++ {
		sum += d[i]
	}
	d[10] = sum % 10
	return join(d)
}

// GenerateVKN builds a valid tax number from nine random digits.
func GenerateVKN(r *rand.Rand) string {
	d := make([]int, 10)
	for i := 0; i < 9; i++ {
		d[i] = r.IntN(10)
	}
	sum := 0
	for i := 1; i <= 9; i++ {
		tmp := (d[i-1] + 10 - i) % 10
		if tmp == 9 {
			sum += 9
		} else {
			sum += (tmp * (1 << (10 - i))) % 9
		}
	}
	d[9] = (10 - sum%10) % 10
	return join(d)
}

func join(d []int) string {
	var b strings.Builder
	for _, x := range d {
		b.WriteString(strconv.Itoa(x))
	}
	return b.String()
}
