//! Risk calculations.

/// A portfolio of positions.
pub struct Portfolio {
    positions: Vec<f64>,
}

impl Portfolio {
    /// Total exposure.
    pub fn exposure(&self) -> f64 {
        std::env::var("RISK_LIMIT").ok();
        0.0
    }
}

fn internal() {}
