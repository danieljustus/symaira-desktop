//! `spf13/pflag` v1.0.10 parsing (interspersed, `ContinueOnError`), including
//! its wording for every parse failure.
use super::quote::{go_quote, go_quote_rune};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FlagKind {
    Bool,
    String,
    Int,
    Duration,
    StringArray,
}

#[derive(Clone, Debug)]
pub struct Flag {
    pub name: String,
    pub shorthand: Option<u8>,
    pub usage: String,
    pub kind: FlagKind,
    pub default: String,
    pub no_opt_default: Option<String>,
    pub hidden: bool,
    pub one_required: bool,
    pub exclusive_groups: Vec<String>,
}

impl Flag {
    /// The flag Cobra's `InitDefaultHelpFlag` adds on execution.
    pub fn help(command: &str) -> Self {
        Self {
            name: "help".to_owned(),
            shorthand: Some(b'h'),
            usage: format!("help for {command}"),
            kind: FlagKind::Bool,
            default: "false".to_owned(),
            no_opt_default: Some("true".to_owned()),
            hidden: false,
            one_required: false,
            exclusive_groups: Vec::new(),
        }
    }

    /// The root's `-v, --version` (`InitDefaultVersionFlag`; `v` is free).
    pub fn version(command: &str) -> Self {
        Self {
            name: "version".to_owned(),
            shorthand: Some(b'v'),
            usage: format!("version for {command}"),
            kind: FlagKind::Bool,
            default: "false".to_owned(),
            no_opt_default: Some("true".to_owned()),
            hidden: false,
            one_required: false,
            exclusive_groups: Vec::new(),
        }
    }

    /// pflag's `InvalidValueError` label.
    fn display_name(&self) -> String {
        match self.shorthand {
            Some(shorthand) => format!("-{}, --{}", char::from(shorthand), self.name),
            None => format!("--{}", self.name),
        }
    }

    fn accepts_multiple(&self) -> bool {
        self.kind == FlagKind::StringArray
    }

    pub fn takes_multiple(&self) -> bool {
        self.accepts_multiple()
    }
}

#[derive(Clone, Default)]
pub struct FlagSet {
    flags: Vec<Flag>,
}

impl FlagSet {
    pub fn add(&mut self, flag: Flag) {
        self.flags.push(flag);
    }

    pub fn add_if_absent(&mut self, flag: Flag) {
        if self.lookup(&flag.name).is_none() {
            self.flags.push(flag);
        }
    }

    pub fn lookup(&self, name: &str) -> Option<&Flag> {
        self.flags.iter().find(|flag| flag.name == name)
    }

    pub fn shorthand(&self, byte: u8) -> Option<&Flag> {
        self.flags.iter().find(|flag| flag.shorthand == Some(byte))
    }

    pub fn index_of(&self, name: &str) -> Option<usize> {
        self.flags.iter().position(|flag| flag.name == name)
    }

    pub fn flags(&self) -> &[Flag] {
        &self.flags
    }

    /// `VisitAll` with `SortFlags` (pflag's default): by name.
    pub fn sorted(&self) -> Vec<&Flag> {
        let mut sorted: Vec<&Flag> = self.flags.iter().collect();
        sorted.sort_by(|left, right| left.name.cmp(&right.name));
        sorted
    }

    /// Cobra's `hasNoOptDefVal`.
    pub fn has_no_opt_def_val(&self, name: &str) -> bool {
        self.lookup(name)
            .is_some_and(|flag| flag.no_opt_default.is_some())
    }

    /// Cobra's `shortHasNoOptDefVal`.
    pub fn short_has_no_opt_def_val(&self, name: &str) -> bool {
        name.bytes()
            .next()
            .and_then(|byte| self.shorthand(byte))
            .is_some_and(|flag| flag.no_opt_default.is_some())
    }
}

#[derive(Clone, Default)]
struct Slot {
    changed: bool,
    bool_value: bool,
    raw: Option<String>,
    array: Vec<String>,
}

/// What `FlagSet.Parse` leaves behind, also after a failure (Go's variables
/// keep every value that was set before the error).
pub struct State {
    set: FlagSet,
    slots: Vec<Slot>,
    pub args: Vec<String>,
    /// `argsLenAtDash`: positional count before `--`, if one was seen.
    pub args_len_at_dash: Option<usize>,
    /// `(index, length, flag name)` of every long-flag token (name and the
    /// separate value token, when one was consumed), into the parsed arguments.
    flag_tokens: Vec<(usize, usize, String)>,
    total: usize,
    position: usize,
}

impl State {
    pub fn changed(&self, name: &str) -> bool {
        self.set
            .index_of(name)
            .is_some_and(|index| self.slots[index].changed)
    }

    /// The bool variable bound to `name` (Go keeps writing it on bad input).
    pub fn bool_value(&self, name: &str) -> bool {
        self.set
            .index_of(name)
            .is_some_and(|index| self.slots[index].bool_value)
    }

    pub fn string_value(&self, name: &str) -> String {
        self.set
            .index_of(name)
            .and_then(|index| self.slots[index].raw.clone())
            .unwrap_or_default()
    }

    pub fn set(&self) -> &FlagSet {
        &self.set
    }

    /// Every long-flag token: `(index, length, name)`.
    pub fn long_tokens(&self) -> &[(usize, usize, String)] {
        &self.flag_tokens
    }

    /// Every token (index, length) that set `name`.
    pub fn tokens_of(&self, name: &str) -> Vec<(usize, usize)> {
        self.flag_tokens
            .iter()
            .filter(|(_, _, flag)| flag == name)
            .map(|(index, length, _)| (*index, *length))
            .collect()
    }
}

fn parse_bool(value: &str) -> Result<bool, String> {
    match value {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(false),
        _ => Err(format!(
            "strconv.ParseBool: parsing {}: invalid syntax",
            go_quote(value)
        )),
    }
}

fn set_flag(set: &FlagSet, slots: &mut [Slot], index: usize, value: &str) -> Result<(), String> {
    let flag = &set.flags()[index];
    let failure = |cause: String| {
        format!(
            "invalid argument {} for {} flag: {cause}",
            go_quote(value),
            go_quote(&flag.display_name())
        )
    };
    let slot = &mut slots[index];
    match flag.kind {
        FlagKind::Bool => match parse_bool(value) {
            Ok(parsed) => slot.bool_value = parsed,
            Err(cause) => {
                // strconv.ParseBool returns false with its error and pflag
                // stores it regardless.
                slot.bool_value = false;
                return Err(failure(cause));
            }
        },
        FlagKind::String => slot.raw = Some(value.to_owned()),
        FlagKind::Int => {
            if let Err(reason) = crate::duplicates_cli::parse_go_int(value) {
                return Err(failure(format!(
                    "strconv.ParseInt: parsing {}: {reason}",
                    go_quote(value)
                )));
            }
            slot.raw = Some(value.to_owned());
        }
        FlagKind::Duration => {
            if let Err(cause) = crate::index_cli::parse_go_duration_nanos(value) {
                return Err(failure(cause));
            }
            slot.raw = Some(value.to_owned());
        }
        FlagKind::StringArray => {
            if !slot.changed {
                slot.array = vec![value.to_owned()];
            } else {
                slot.array.push(value.to_owned());
            }
            slot.raw = Some(value.to_owned());
        }
    }
    slot.changed = true;
    Ok(())
}

/// The rune pflag reports for a failing shorthand: `rune(string(c)[0])`.
fn shorthand_rune(byte: u8) -> char {
    match byte {
        0..=0x7f => char::from(byte),
        0x80..=0xbf => '\u{c2}',
        _ => '\u{c3}',
    }
}

/// `FlagSet.Parse(args)` with `interspersed = true`.
pub fn parse(set: &FlagSet, args: &[String]) -> (State, Result<(), String>) {
    let mut state = State {
        set: set.clone(),
        slots: vec![Slot::default(); set.flags().len()],
        args: Vec::new(),
        args_len_at_dash: None,
        flag_tokens: Vec::new(),
        total: args.len(),
        position: 0,
    };
    for (index, flag) in set.flags().iter().enumerate() {
        if flag.kind == FlagKind::Bool {
            state.slots[index].bool_value = flag.default == "true";
        }
    }
    let result = parse_args(&mut state, args);
    (state, result)
}

fn parse_args(state: &mut State, mut args: &[String]) -> Result<(), String> {
    while let Some((current, rest)) = args.split_first() {
        state.position = state.total - args.len();
        args = rest;
        let bytes = current.as_bytes();
        if bytes.is_empty() || bytes[0] != b'-' || bytes.len() == 1 {
            state.args.push(current.clone());
            continue;
        }
        if bytes[1] == b'-' {
            if bytes.len() == 2 {
                state.args_len_at_dash = Some(state.args.len());
                state.args.extend(args.iter().cloned());
                return Ok(());
            }
            args = parse_long(state, current, args)?;
        } else {
            args = parse_short(state, current, args)?;
        }
    }
    Ok(())
}

fn parse_long<'a>(
    state: &mut State,
    argument: &str,
    mut rest: &'a [String],
) -> Result<&'a [String], String> {
    let name = &argument[2..];
    if name.is_empty() || name.starts_with('-') || name.starts_with('=') {
        return Err(format!("bad flag syntax: {argument}"));
    }
    let (name, explicit) = match name.split_once('=') {
        Some((name, value)) => (name, Some(value)),
        None => (name, None),
    };
    let Some(index) = state.set.index_of(name) else {
        return Err(format!("unknown flag: --{name}"));
    };
    let flag = &state.set.flags()[index];
    let mut length = 1;
    let value = if let Some(value) = explicit {
        value.to_owned()
    } else if let Some(no_opt) = &flag.no_opt_default {
        no_opt.clone()
    } else if let Some((value, remaining)) = rest.split_first() {
        rest = remaining;
        length = 2;
        value.clone()
    } else {
        return Err(format!("flag needs an argument: --{name}"));
    };
    let set = state.set.clone();
    state
        .flag_tokens
        .push((state.position, length, name.to_owned()));
    set_flag(&set, &mut state.slots, index, &value)?;
    Ok(rest)
}

fn parse_short<'a>(
    state: &mut State,
    argument: &str,
    rest: &'a [String],
) -> Result<&'a [String], String> {
    let mut shorthands = &argument[1..];
    let mut remaining = rest;
    while !shorthands.is_empty() {
        let (next_shorthands, next_rest) = parse_single_short(state, shorthands, rest)?;
        shorthands = next_shorthands;
        remaining = next_rest;
    }
    Ok(remaining)
}

fn parse_single_short<'a, 'b>(
    state: &mut State,
    shorthands: &'b str,
    args: &'a [String],
) -> Result<(&'b str, &'a [String]), String> {
    if shorthands.starts_with("test.") {
        return Ok(("", args));
    }
    let bytes = shorthands.as_bytes();
    let byte = bytes[0];
    // A multi-byte first character cannot be a shorthand; pflag slices bytes.
    let after = shorthands.get(1..).unwrap_or("");
    let Some(flag_index) = state
        .set
        .flags()
        .iter()
        .position(|flag| flag.shorthand == Some(byte))
    else {
        return Err(format!(
            "unknown shorthand flag: {} in -{shorthands}",
            go_quote_rune(shorthand_rune(byte))
        ));
    };
    let flag = state.set.flags()[flag_index].clone();
    let mut next_shorthands = after;
    let mut next_args = args;
    let value: String;
    if bytes.len() > 2 && bytes[1] == b'=' {
        value = shorthands[2..].to_owned();
        next_shorthands = "";
    } else if let Some(no_opt) = &flag.no_opt_default {
        value = no_opt.clone();
    } else if bytes.len() > 1 {
        value = shorthands[1..].to_owned();
        next_shorthands = "";
    } else if let Some((first, remaining)) = args.split_first() {
        value = first.clone();
        next_args = remaining;
    } else {
        return Err(format!(
            "flag needs an argument: {} in -{shorthands}",
            go_quote_rune(shorthand_rune(byte))
        ));
    }
    let set = state.set.clone();
    if flag.name == "help"
        && !state
            .flag_tokens
            .iter()
            .any(|(index, _, name)| *index == state.position && name == "help")
    {
        state
            .flag_tokens
            .push((state.position, 1, "help".to_owned()));
    }
    set_flag(&set, &mut state.slots, flag_index, &value)?;
    Ok((next_shorthands, next_args))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn set() -> FlagSet {
        let mut set = FlagSet::default();
        set.add(Flag {
            name: "dir".into(),
            shorthand: None,
            usage: String::new(),
            kind: FlagKind::String,
            default: String::new(),
            no_opt_default: None,
            hidden: false,
            one_required: false,
            exclusive_groups: Vec::new(),
        });
        set.add(Flag::help("ls"));
        set
    }

    fn strings(values: &[&str]) -> Vec<String> {
        values.iter().map(|value| (*value).to_owned()).collect()
    }

    #[test]
    fn long_short_and_terminator() {
        let (state, result) = parse(&set(), &strings(&["a", "--dir", "x", "-h", "--", "--dir"]));
        assert!(result.is_ok());
        assert_eq!(state.args, ["a", "--dir"]);
        assert_eq!(state.string_value("dir"), "x");
        assert!(state.bool_value("help"));
        assert_eq!(state.args_len_at_dash, Some(1));
    }

    #[test]
    fn failures_use_pflag_wording() {
        let cases = [
            (vec!["--bogus"], "unknown flag: --bogus"),
            (vec!["--dir"], "flag needs an argument: --dir"),
            (vec!["-x"], "unknown shorthand flag: 'x' in -x"),
            (vec!["-hx"], "unknown shorthand flag: 'x' in -x"),
            (vec!["---"], "bad flag syntax: ---"),
            (vec!["--=a"], "bad flag syntax: --=a"),
            (
                vec!["--help=maybe"],
                "invalid argument \"maybe\" for \"-h, --help\" flag: strconv.ParseBool: parsing \"maybe\": invalid syntax",
            ),
            (vec!["-é"], "unknown shorthand flag: 'Ã' in -é"),
        ];
        for (arguments, expected) in cases {
            let (_, result) = parse(&set(), &strings(&arguments));
            assert_eq!(result.unwrap_err(), expected, "{arguments:?}");
        }
    }

    #[test]
    fn a_value_flag_consumes_a_following_flag_like_word() {
        let (state, result) = parse(&set(), &strings(&["--dir", "--help", "-h"]));
        assert!(result.is_ok());
        assert_eq!(state.string_value("dir"), "--help");
        assert!(state.bool_value("help"));
        assert_eq!(state.long_tokens()[0].1, 2);
    }

    #[test]
    fn bool_value_is_false_after_a_failed_parse_like_go() {
        let (state, result) = parse(&set(), &strings(&["-h", "--help=maybe"]));
        assert!(result.is_err());
        assert!(!state.bool_value("help"));
    }

    #[test]
    fn test_prefix_shorthand_is_ignored_like_pflag() {
        let (state, result) = parse(&set(), &strings(&["-test.v"]));
        assert!(result.is_ok());
        assert!(state.args.is_empty());
    }
}
