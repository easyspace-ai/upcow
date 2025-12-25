//! Configuration file support
//!
//! Reads configuration from config.yml file with environment variable overrides

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use std::path::PathBuf;

/// Main configuration structure
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AppConfig {
    pub proxy: ProxyConfig,
    pub polymarket: PolymarketConfig,
    pub polygon: PolygonConfig,
    pub trading: TradingConfig,
    pub btc_1h_pair_trading: Option<Btc1hPairTradingConfig>,
    pub poly_sniper: Option<PolySniperConfig>,
    pub circuit_breaker: Option<CircuitBreakerConfig>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProxyConfig {
    pub enabled: bool,
    pub address: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PolymarketConfig {
    pub private_key: String,
    pub funder: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PolygonConfig {
    pub api_key: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TradingConfig {
    pub dry_run: bool,
    pub log_level: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Btc1hPairTradingConfig {
    pub profit_target: i64,
    pub contracts: i64,
    pub max_rounds: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PolySniperConfig {
    pub size: f64,
    pub edge: i64,
    pub aggro_edge: i64,
    pub vol: f64,
    pub arb: bool,
    pub arb_edge: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CircuitBreakerConfig {
    pub enabled: bool,
    pub max_position_per_market: i64,
    pub max_total_position: i64,
    pub max_daily_loss: f64,
    pub max_consecutive_errors: u32,
    pub cooldown_secs: u64,
}

impl AppConfig {
    /// Load configuration from file with environment variable overrides
    pub fn load() -> Result<Self> {
        // Try to load from config.yml
        let config_path = PathBuf::from("config.yml");
        let mut config: AppConfig = if config_path.exists() {
            let content = std::fs::read_to_string(&config_path)
                .context("Failed to read config.yml")?;
            serde_yaml::from_str(&content)
                .context("Failed to parse config.yml")?
        } else {
            // Default config if file doesn't exist
            Self::default()
        };

        // Override with environment variables
        config.apply_env_overrides();

        Ok(config)
    }

    /// Apply environment variable overrides
    fn apply_env_overrides(&mut self) {
        // Proxy settings
        if let Ok(enabled) = std::env::var("PROXY_ENABLED") {
            self.proxy.enabled = enabled == "1" || enabled == "true";
        }
        if let Ok(addr) = std::env::var("PROXY_ADDRESS") {
            self.proxy.address = addr;
        }

        // Polymarket settings
        if let Ok(private_key) = std::env::var("POLY_PRIVATE_KEY") {
            self.polymarket.private_key = private_key;
        }
        if let Ok(funder) = std::env::var("POLY_FUNDER") {
            self.polymarket.funder = funder;
        }

        // Polygon settings
        if let Ok(api_key) = std::env::var("POLYGON_API_KEY") {
            self.polygon.api_key = api_key;
        }

        // Trading settings
        if let Ok(dry_run) = std::env::var("DRY_RUN") {
            self.trading.dry_run = dry_run == "1" || dry_run == "true";
        }
        if let Ok(log_level) = std::env::var("RUST_LOG") {
            self.trading.log_level = log_level;
        }
    }

    /// Get proxy URL if enabled
    pub fn proxy_url(&self) -> Option<String> {
        if self.proxy.enabled {
            Some(format!("http://{}", self.proxy.address))
        } else {
            None
        }
    }
}

impl Default for AppConfig {
    fn default() -> Self {
        Self {
            proxy: ProxyConfig {
                enabled: true,
                address: "127.0.0.1:15236".to_string(),
            },
            polymarket: PolymarketConfig {
                private_key: String::new(),
                funder: String::new(),
            },
            polygon: PolygonConfig {
                api_key: "o2Jm26X52_0tRq2W7V5JbsCUXdMjL7qk".to_string(),
            },
            trading: TradingConfig {
                dry_run: true,
                log_level: "info".to_string(),
            },
            btc_1h_pair_trading: Some(Btc1hPairTradingConfig {
                profit_target: 3,
                contracts: 10,
                max_rounds: 2,
            }),
            poly_sniper: Some(PolySniperConfig {
                size: 20.0,
                edge: 5,
                aggro_edge: 15,
                vol: 58.0,
                arb: false,
                arb_edge: 2,
            }),
            circuit_breaker: Some(CircuitBreakerConfig {
                enabled: true,
                max_position_per_market: 50000,
                max_total_position: 100000,
                max_daily_loss: 500.0,
                max_consecutive_errors: 5,
                cooldown_secs: 300,
            }),
        }
    }
}

/// Helper function to create reqwest::Client with proxy support
pub fn create_http_client() -> Result<reqwest::Client> {
    let config = AppConfig::load()?;
    create_http_client_with_proxy(config.proxy_url())
}

/// Create reqwest::Client with optional proxy
pub fn create_http_client_with_proxy(proxy_url: Option<String>) -> Result<reqwest::Client> {
    let mut builder = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(10));

    if let Some(proxy) = proxy_url {
        builder = builder.proxy(reqwest::Proxy::all(&proxy)?);
    }

    Ok(builder.build()?)
}

/// Create reqwest::Client with custom timeout and optional proxy
pub fn create_http_client_with_timeout(timeout_secs: u64, proxy_url: Option<String>) -> Result<reqwest::Client> {
    let mut builder = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(timeout_secs));

    if let Some(proxy) = proxy_url {
        builder = builder.proxy(reqwest::Proxy::all(&proxy)?);
    }

    Ok(builder.build()?)
}

/// Create reqwest::Client builder with proxy support
pub fn create_client_builder() -> Result<reqwest::ClientBuilder> {
    let config = AppConfig::load()?;
    let mut builder = reqwest::Client::builder();
    
    if let Some(proxy) = config.proxy_url() {
        builder = builder.proxy(reqwest::Proxy::all(&proxy)?);
    }
    
    Ok(builder)
}

/// Get proxy URL from config
pub fn get_proxy_url() -> Option<String> {
    AppConfig::load().ok().and_then(|c| c.proxy_url())
}

