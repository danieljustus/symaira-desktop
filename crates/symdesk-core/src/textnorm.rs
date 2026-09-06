#![deny(unsafe_code)]

//! Deterministic OCR text normalization and language hints.

const CONTINUATION_WORDS: &[&str] = &["und", "oder", "bis", "wie", "als"];
const GERMAN_SIGNAL_WORDS: &[&str] = &[
    "der", "die", "das", "und", "ist", "nicht", "mit", "für", "auf", "eine", "einer", "einem",
    "einen", "werden", "wurde", "sie", "wir", "ich", "dem", "den", "des", "im", "am", "zu", "von",
    "sich", "auch", "oder", "aber", "wenn", "dann", "noch", "nur", "schon", "kann", "muss", "soll",
    "werden", "zwischen", "gegen", "ohne", "über",
];
const ENGLISH_SIGNAL_WORDS: &[&str] = &[
    "the", "and", "is", "are", "was", "were", "of", "to", "in", "for", "with", "not", "this",
    "that", "these", "those", "you", "your", "we", "they", "their", "from", "have", "has", "had",
    "will", "would", "can", "could", "should", "between", "without", "about", "which", "when",
];

pub const LANG_GERMAN: &str = "deu";
pub const LANG_ENGLISH: &str = "eng";

#[must_use]
pub fn dehyphenate(input: &str) -> String {
    if !input.contains('\n') {
        return input.to_owned();
    }
    let mut lines: Vec<String> = input.split('\n').map(str::to_owned).collect();
    let mut index = 0;
    while index + 1 < lines.len() {
        let left = lines[index].trim_end_matches([' ', '\t']).to_owned();
        let right = &lines[index + 1];
        let unindented = right.trim_start_matches([' ', '\t']) == right;
        if left.ends_with('-')
            && unindented
            && starts_lowercase_letter(right)
            && !starts_with_continuation_word(right)
            && letter_before_hyphen(&left)
        {
            lines[index] = format!("{}{}", left.trim_end_matches('-'), right);
            lines.remove(index + 1);
        } else {
            index += 1;
        }
    }
    lines.join("\n")
}

#[must_use]
pub fn detect_language(text: &str) -> &'static str {
    let words = words_of(text);
    if words.len() < 20 {
        return "";
    }
    let mut german = 0_i32;
    let mut english = 0_i32;
    for word in &words {
        if GERMAN_SIGNAL_WORDS.contains(&word.as_str()) {
            german += 1;
        }
        if ENGLISH_SIGNAL_WORDS.contains(&word.as_str()) {
            english += 1;
        }
    }
    for character in text.chars() {
        if matches!(character, 'ä' | 'ö' | 'ü' | 'Ä' | 'Ö' | 'Ü' | 'ß') {
            german += 3;
        }
    }
    if german >= english + 3 && german >= 5 {
        LANG_GERMAN
    } else if english > german + 3 {
        LANG_ENGLISH
    } else {
        ""
    }
}

fn starts_lowercase_letter(value: &str) -> bool {
    value.chars().next().is_some_and(char::is_lowercase)
}

fn letter_before_hyphen(value: &str) -> bool {
    value
        .strip_suffix('-')
        .and_then(|prefix| prefix.chars().next_back())
        .is_some_and(char::is_alphabetic)
}

fn starts_with_continuation_word(value: &str) -> bool {
    let word: String = value
        .chars()
        .take_while(|character| character.is_alphabetic())
        .collect();
    CONTINUATION_WORDS.contains(&word.to_lowercase().as_str())
}

fn words_of(text: &str) -> Vec<String> {
    let mut words = Vec::new();
    let mut current = String::new();
    for character in text.chars() {
        if character.is_alphabetic() {
            current.extend(character.to_lowercase());
        } else if !current.is_empty() {
            words.push(std::mem::take(&mut current));
        }
    }
    if !current.is_empty() {
        words.push(current);
    }
    words
}
