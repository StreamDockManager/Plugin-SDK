//! Byte percent encoding shared by otherwise independent redaction policies
pub(crate) fn encoded(value: &str) -> String {
    value
        .bytes()
        .map(|b| {
            if b.is_ascii_alphanumeric() || b"-_.~".contains(&b) {
                (b as char).to_string()
            } else {
                format!("%{b:02X}")
            }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    #[test]
    // Verify unicode and reserved bytes
    fn unicode_and_reserved_bytes() {
        assert_eq!(
            super::encoded("AZaz09-_.~ /+%é🦀"),
            "AZaz09-_.~%20%2F%2B%25%C3%A9%F0%9F%A6%80"
        );
    }
}
