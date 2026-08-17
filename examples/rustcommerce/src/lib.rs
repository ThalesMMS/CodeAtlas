//! Safe, static Rust commerce fixture for CodeAtlas evaluation.

pub mod models;
pub mod repository;
pub mod service;

pub use models::Order;
pub use repository::{MemoryRepository, Repository};
pub use service::OrderService;
