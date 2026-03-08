package asagumo

import (
	"regexp"
	"strconv"
	"strings"
)

func NormalizeNumber(input string) (int, bool) {
	// 全角数字を半角に変換
	input = strings.Map(func(r rune) rune {
		if r >= '０' && r <= '９' {
			return r - '０' + '0'
		}
		return r
	}, input)

	// 数字の塊を探す
	reNum := regexp.MustCompile(`[0-9]+`)
	if match := reNum.FindString(input); match != "" {
		val, _ := strconv.Atoi(match)
		return val, true
	}

	// 漢数字の塊を探す
	reKanji := regexp.MustCompile(`[一二三四五六七八九十]+`)
	if match := reKanji.FindString(input); match != "" {
		return parseKanjiNumber(match)
	}

	return 0, false
}

func parseKanjiNumber(s string) (int, bool) {
	kanjiMap := map[rune]int{
		'一': 1, '二': 2, '三': 3, '四': 4, '五': 5,
		'六': 6, '七': 7, '八': 8, '九': 9,
	}

	res := 0
	tmp := 0
	for _, r := range s {
		if val, ok := kanjiMap[r]; ok {
			tmp = val
		} else if r == '十' {
			if tmp == 0 {
				tmp = 1
			}
			res += tmp * 10
			tmp = 0
		} else {
			return 0, false
		}
	}
	res += tmp
	return res, res > 0
}
