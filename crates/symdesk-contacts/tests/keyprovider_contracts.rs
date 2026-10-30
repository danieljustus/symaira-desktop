use serde_json::{Value, json};
use std::{cell::RefCell, rc::Rc};
use symdesk_contacts::keyprovider::{self as keys, Error, Key, Terminal};
fn decode(v: &Value) -> Vec<u8> {
    let s = v.as_str().unwrap();
    assert_eq!(s.len() % 2, 0);
    (0..s.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&s[i..i + 2], 16).unwrap())
        .collect()
}
fn encode(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}
struct NonTerminal;
impl Terminal for NonTerminal {
    fn is_terminal(&self) -> bool {
        false
    }
    fn read_password(&mut self, _: &str) -> Result<Vec<u8>, String> {
        panic!("non-TTY must never read")
    }
}
#[test]
fn go_contacts_keyprovider_contract() {
    let fixture: Value = serde_json::from_str(include_str!(
        "../../../testdata/port/contacts/keyprovider.json"
    ))
    .unwrap();
    let cases = fixture["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 40);
    for case in cases {
        let input = &case["input"];
        let mut visits = Value::Null;
        let result = match case["name"].as_str().unwrap() {
            "static" => keys::resolve_static(&decode(&input["key_hex"])),
            "env" => keys::resolve_env(input["name"].as_str().unwrap(), |name| {
                assert_eq!(
                    name,
                    if input["name"] == "" {
                        keys::DEFAULT_ENV_VAR
                    } else {
                        input["name"].as_str().unwrap()
                    }
                );
                Some(input["value"].as_str().unwrap().as_bytes().to_vec())
            }),
            "default" => keys::resolve_default(
                &decode(&input["explicit_hex"]),
                |name| {
                    assert_eq!(name, keys::DEFAULT_ENV_VAR);
                    Some(input["env"].as_str().unwrap().as_bytes().to_vec())
                },
                |_, _| panic!("default should not reach symvault"),
                |_| panic!("default should not reach terminal"),
                input["confirm"].as_bool().unwrap(),
            ),
            "terminal_non_tty" => keys::resolve_terminal(&mut NonTerminal, "", true),
            "symvault_absent" => keys::resolve_symvault("", |_, _| Err(Error::Unavailable)),
            "symvault" => keys::resolve_symvault(input["name"].as_str().unwrap(), |op, name| {
                assert_eq!(format!("{op}\n{name}"), case["args"].as_str().unwrap());
                if input["fail"].as_bool().unwrap() {
                    Err(Error::Failure("fixture process failed".to_owned()))
                } else {
                    Ok(decode(&input["stdout_hex"]))
                }
            }),
            "chain" => {
                let observed = Rc::new(RefCell::new(Vec::new()));
                let mut providers: Vec<Box<dyn FnMut() -> Result<Key, Error>>> = input["states"]
                    .as_array()
                    .unwrap()
                    .iter()
                    .enumerate()
                    .map(|(i, state)| {
                        let observed = Rc::clone(&observed);
                        let state = state.as_str().unwrap().to_owned();
                        Box::new(move || {
                            observed.borrow_mut().push(i);
                            match state.as_str() {
                                "unavailable" | "wrapped_unavailable" => Err(Error::Unavailable),
                                "fatal" => {
                                    Err(Error::Failure("fixture provider failed".to_owned()))
                                }
                                "ok" => Ok(Key {
                                    passphrase: b"fixture-chain".to_vec(),
                                    source: "fixture".to_owned(),
                                }),
                                "empty_ok" => Ok(Key {
                                    passphrase: Vec::new(),
                                    source: "empty".to_owned(),
                                }),
                                _ => panic!("unknown provider state"),
                            }
                        }) as Box<dyn FnMut() -> Result<Key, Error>>
                    })
                    .collect();
                let result = keys::resolve_chain(
                    providers
                        .iter_mut()
                        .map(|p| &mut **p as &mut dyn FnMut() -> Result<Key, Error>),
                );
                visits = json!(*observed.borrow());
                result
            }
            other => panic!("{other}"),
        };
        let actual = match result {
            Ok(key) => {
                json!({"key_hex":encode(&key.passphrase),"key_nil":false,"source":key.source,"error":"","unavailable":false})
            }
            Err(e) => {
                json!({"key_hex":"","key_nil":true,"source":"","error":e.to_string(),"unavailable":e == Error::Unavailable})
            }
        };
        for field in ["key_hex", "key_nil", "source", "error", "unavailable"] {
            assert_eq!(
                actual[field], case[field],
                "{} {input}: {field}",
                case["name"]
            );
        }
        assert_eq!(visits, case["visits"]);
    }
}
