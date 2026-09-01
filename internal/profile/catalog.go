package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"

	"javbeaconsubs/internal/config"
)

const (
	RecognitionFormat  = "javbeaconsubs-japanese-recognition-vocabulary"
	RecognitionVersion = 1
	GlossaryFormat     = "javbeaconsubs-translation-glossary"
	GlossaryVersion    = 2
)

type RecognitionTerm struct {
	Term   string `json:"term"`
	Source string `json:"source,omitempty"`
	Notes  string `json:"notes,omitempty"`
}

func (value *RecognitionTerm) UnmarshalJSON(data []byte) error {
	type plain RecognitionTerm
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name := range fields {
		if name != "term" && name != "source" && name != "notes" {
			return fmt.Errorf("recognition term contains unsupported field %q; English translations belong in the translation glossary", name)
		}
	}
	return json.Unmarshal(data, (*plain)(value))
}

type RecognitionVocabulary struct {
	Format                 string                       `json:"format"`
	Version                int                          `json:"version"`
	Description            string                       `json:"description,omitempty"`
	Profiles               json.RawMessage              `json:"profiles,omitempty"`
	Activation             json.RawMessage              `json:"activation,omitempty"`
	Rules                  json.RawMessage              `json:"rules,omitempty"`
	ObservedButNotPromoted json.RawMessage              `json:"observed_but_not_promoted,omitempty"`
	Scopes                 map[string][]RecognitionTerm `json:"scopes"`
	TitleOrSeriesOverrides RecognitionOverrides         `json:"title_or_series_overrides,omitempty"`
}

type TranslationTerm struct {
	Japanese string `json:"ja"`
	English  string `json:"en"`
	Notes    string `json:"notes,omitempty"`
}

type TranslationGlossary struct {
	Format                 string                       `json:"format"`
	Version                int                          `json:"version"`
	Description            string                       `json:"description,omitempty"`
	Precedence             json.RawMessage              `json:"precedence,omitempty"`
	Profiles               json.RawMessage              `json:"profiles,omitempty"`
	ImportRules            json.RawMessage              `json:"import_rules,omitempty"`
	Scopes                 map[string][]TranslationTerm `json:"scopes"`
	TitleOrSeriesOverrides TranslationOverrides         `json:"title_or_series_overrides,omitempty"`
}

type RecognitionOverrides map[string][]RecognitionTerm
type TranslationOverrides map[string][]TranslationTerm

func (value *RecognitionOverrides) UnmarshalJSON(data []byte) error {
	return decodeOverrides(data, (*map[string][]RecognitionTerm)(value))
}

func (value *TranslationOverrides) UnmarshalJSON(data []byte) error {
	return decodeOverrides(data, (*map[string][]TranslationTerm)(value))
}

func decodeOverrides[T any](data []byte, output *map[string][]T) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" {
		*output = map[string][]T{}
		return nil
	}
	return json.Unmarshal(data, output)
}

func LoadRecognition(path string) (RecognitionVocabulary, error) {
	var value RecognitionVocabulary
	if err := loadJSON(path, &value); err != nil {
		return value, err
	}
	return value, ValidateRecognition(value)
}

func LoadTranslationGlossary(path string) (TranslationGlossary, error) {
	var value TranslationGlossary
	if err := loadJSON(path, &value); err != nil {
		return value, err
	}
	return value, ValidateTranslationGlossary(value)
}

func loadJSON(path string, output any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func ValidateRecognition(value RecognitionVocabulary) error {
	if value.Format != RecognitionFormat || value.Version != RecognitionVersion {
		return fmt.Errorf("recognition vocabulary must use format %q version %d", RecognitionFormat, RecognitionVersion)
	}
	if err := requireScopes(value.Scopes); err != nil {
		return fmt.Errorf("recognition vocabulary: %w", err)
	}
	for scope, terms := range value.Scopes {
		if err := validateRecognitionTerms(scope, terms); err != nil {
			return err
		}
	}
	for name, terms := range value.TitleOrSeriesOverrides {
		if strings.TrimSpace(name) == "" {
			return errors.New("recognition vocabulary contains a blank title override")
		}
		if err := validateRecognitionTerms("title override "+name, terms); err != nil {
			return err
		}
	}
	return nil
}

func validateRecognitionTerms(scope string, terms []RecognitionTerm) error {
	seen := map[string]bool{}
	for index, item := range terms {
		term := strings.TrimSpace(item.Term)
		if term == "" {
			return fmt.Errorf("recognition vocabulary scope %s entry %d has a blank term", scope, index+1)
		}
		if !containsRecognitionCharacters(term) {
			return fmt.Errorf("recognition vocabulary scope %s term %q is not Japanese recognition vocabulary", scope, term)
		}
		if seen[term] {
			return fmt.Errorf("recognition vocabulary scope %s contains duplicate term %q", scope, term)
		}
		seen[term] = true
	}
	return nil
}

func ValidateTranslationGlossary(value TranslationGlossary) error {
	if value.Format != GlossaryFormat || value.Version != GlossaryVersion {
		return fmt.Errorf("translation glossary must use format %q version %d", GlossaryFormat, GlossaryVersion)
	}
	if err := requireScopes(value.Scopes); err != nil {
		return fmt.Errorf("translation glossary: %w", err)
	}
	for scope, terms := range value.Scopes {
		if err := validateTranslationTerms(scope, terms); err != nil {
			return err
		}
	}
	for name, terms := range value.TitleOrSeriesOverrides {
		if strings.TrimSpace(name) == "" {
			return errors.New("translation glossary contains a blank title override")
		}
		if err := validateTranslationTerms("title override "+name, terms); err != nil {
			return err
		}
	}
	return nil
}

func validateTranslationTerms(scope string, terms []TranslationTerm) error {
	seen := map[string]bool{}
	for index, item := range terms {
		ja, en := strings.TrimSpace(item.Japanese), strings.TrimSpace(item.English)
		if ja == "" || en == "" {
			return fmt.Errorf("translation glossary scope %s entry %d requires ja and en", scope, index+1)
		}
		if seen[ja] {
			return fmt.Errorf("translation glossary scope %s contains duplicate Japanese term %q", scope, ja)
		}
		seen[ja] = true
	}
	return nil
}

func requireScopes[T any](scopes map[string][]T) error {
	for _, name := range []string{"global", "jav", "giga"} {
		if _, ok := scopes[name]; !ok {
			return fmt.Errorf("missing %s scope", name)
		}
	}
	for name := range scopes {
		if name != "global" && name != "jav" && name != "giga" {
			return fmt.Errorf("unsupported scope %q", name)
		}
	}
	return nil
}

func containsRecognitionCharacters(value string) bool {
	for _, r := range value {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) || unicode.IsDigit(r) || unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func ActiveScopes(profileValue string) []string {
	profileValue = config.NormalizeProfile(profileValue)
	if profileValue == "jav" || profileValue == "giga" {
		return []string{"global", profileValue}
	}
	return []string{"global"}
}

func (value TranslationGlossary) Resolve(profileValue, title string) (*config.StructuredGlossary, []string) {
	terms := map[string]string{}
	scopes := ActiveScopes(profileValue)
	for _, scope := range scopes {
		for _, item := range value.Scopes[scope] {
			terms[strings.TrimSpace(item.Japanese)] = strings.TrimSpace(item.English)
		}
	}
	title = strings.TrimSpace(title)
	if title != "" {
		if override, ok := value.TitleOrSeriesOverrides[title]; ok {
			for _, item := range override {
				terms[strings.TrimSpace(item.Japanese)] = strings.TrimSpace(item.English)
			}
			scopes = append(scopes, "title_override")
		}
	}
	return &config.StructuredGlossary{Terms: terms}, scopes
}

func (value RecognitionVocabulary) Terms(profileValue, title string, limit int) ([]string, []string) {
	if limit < 0 {
		limit = 0
	}
	scopes := ActiveScopes(profileValue)
	seen := map[string]bool{}
	result := make([]string, 0)
	add := func(items []RecognitionTerm) {
		for _, item := range items {
			term := strings.TrimSpace(item.Term)
			if term != "" && !seen[term] {
				seen[term] = true
				result = append(result, term)
			}
		}
	}
	title = strings.TrimSpace(title)
	if title != "" {
		if override, ok := value.TitleOrSeriesOverrides[title]; ok {
			add(override)
			scopes = append(scopes, "title_override")
		}
	}
	profileValue = config.NormalizeProfile(profileValue)
	if profileValue == "jav" || profileValue == "giga" {
		add(value.Scopes[profileValue])
	}
	add(value.Scopes["global"])
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, scopes
}

func SortedScopeNames(scopes map[string][]TranslationTerm) []string {
	result := make([]string, 0, len(scopes))
	for name := range scopes {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

var ErrUnsupportedImport = errors.New("unsupported glossary or vocabulary import")
