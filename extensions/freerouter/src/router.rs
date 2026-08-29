use std::collections::HashMap;
use std::time::{Duration, Instant};

#[derive(Debug, Clone)]
struct CoolEntry {
    at: Instant,
    ttl: Duration,
}

/// TTL cooldown registry for free models.
#[derive(Debug)]
pub struct FreeRouter {
    models: Vec<String>,
    exhausted_ttl: Duration,
    slow_ttl: Duration,
    exhausted: HashMap<String, CoolEntry>,
}

impl FreeRouter {
    pub fn new(models: Vec<String>, exhausted_ttl_ms: u64, slow_ttl_ms: u64) -> Self {
        Self {
            models,
            exhausted_ttl: Duration::from_millis(exhausted_ttl_ms),
            slow_ttl: Duration::from_millis(slow_ttl_ms),
            exhausted: HashMap::new(),
        }
    }

    pub fn set_models(&mut self, models: Vec<String>) {
        self.models = models;
        self.exhausted
            .retain(|id, _| self.models.iter().any(|m| m == id));
    }

    pub fn next_models(&mut self, count: usize) -> Vec<String> {
        let now = Instant::now();
        let mut result = Vec::new();
        let mut expired = Vec::new();
        for id in &self.models {
            if result.len() >= count {
                break;
            }
            if let Some(entry) = self.exhausted.get(id) {
                if now.duration_since(entry.at) < entry.ttl {
                    continue;
                }
                expired.push(id.clone());
            }
            result.push(id.clone());
        }
        for id in expired {
            self.exhausted.remove(&id);
        }
        result
    }

    pub fn mark_exhausted(&mut self, id: &str) {
        if !self.models.iter().any(|m| m == id) {
            return;
        }
        self.exhausted.insert(
            id.to_string(),
            CoolEntry {
                at: Instant::now(),
                ttl: self.exhausted_ttl,
            },
        );
    }

    pub fn mark_slow(&mut self, id: &str) {
        if !self.models.iter().any(|m| m == id) {
            return;
        }
        if let Some(existing) = self.exhausted.get(id) {
            if existing.ttl >= self.exhausted_ttl {
                return;
            }
        }
        self.exhausted.insert(
            id.to_string(),
            CoolEntry {
                at: Instant::now(),
                ttl: self.slow_ttl,
            },
        );
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::thread;

    #[test]
    fn preference_order_and_cooldown() {
        let mut r = FreeRouter::new(vec!["a".into(), "b".into(), "c".into()], 50, 10);
        assert_eq!(r.next_models(10), vec!["a", "b", "c"]);
        r.mark_exhausted("b");
        assert_eq!(r.next_models(10), vec!["a", "c"]);
        thread::sleep(Duration::from_millis(60));
        assert_eq!(r.next_models(10), vec!["a", "b", "c"]);
    }

    #[test]
    fn mark_slow_does_not_downgrade() {
        let mut r = FreeRouter::new(vec!["a".into()], 1000, 10);
        r.mark_exhausted("a");
        r.mark_slow("a");
        assert!(r.next_models(10).is_empty());
    }
}
