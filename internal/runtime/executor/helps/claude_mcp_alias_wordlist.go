package helps

import (
	_ "embed"
	"strings"
)

//go:embed claude_bip39_words.txt
var rawBIP39EnglishWords string

// claudeMCPAliasEnglishWords contains the standard BIP-39 English wordlist (2048 words).
var claudeMCPAliasEnglishWords = strings.Fields(rawBIP39EnglishWords)
