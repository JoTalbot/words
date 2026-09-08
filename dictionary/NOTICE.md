# Dictionary data notices

M0 snapshot **v2** word lists are derived from third-party open sources.
Attribution and licensing below. Word lists are pre-production assets; a
legal review is required before any public distribution or commercial use.

| Language | File | Derived from | License | Notes |
|---|---|---|---|---|
| en | `server/internal/dictionary/data/en.words` | SCOWL (via LibreOffice `en_US.dic`, 2020.12.07), wordlist.aspell.net | Permissive (SCOWL license, see https://wordlist.aspell.net; MIT-style) | Filtered: `[a-z]{3,9}`, lowercase, 35 502 words |
| ru | `server/internal/dictionary/data/ru.words` | danakt/russian-words (`russian.txt`), https://github.com/danakt/russian-words | MIT (c) 2020 Danakt Frost | Filtered: Cyrillic `[а-я]{3,9}` with ё→е fold, 482 731 forms |
| uk | `server/internal/dictionary/data/uk.words` | ukrainian dictionary `uk_UA` (derived from brown-uk/dict_uk), via LibreOffice dictionaries | GPL-3.0+ (brown-uk/dict_uk); **pre-production only, legal review required** | Filtered: `[абвгґдеєжзиіїйклмнопрстуфхцчшщьюя]{3,9}`, 123 595 forms |

Rebuild procedure (data provenance):

```bash
# en: download en_US.dic from LibreOffice/dictionaries, keep "word[/flags]" tokens
# ru: download russian.txt from danakt/russian-words (windows-1251), lowercase, fold ё->е
# uk: download uk_UA.dic from LibreOffice/dictionaries, keep tokens
# filter each: alphabet membership per language, length 3..9, sort, unique
# then refresh internal/dictionary/data/manifest.json sha256 (see dictionary tests)
```

The fixture dictionaries used by rule tests live in the same files; they are
not separate from the shipped snapshots by design (M0 keeps one source of
word truth).
