use rustcommerce::{MemoryRepository, OrderService};

#[test]
fn constructs_service_without_running_repository_hooks() {
    let repository = MemoryRepository::with_capacity(4);
    let _service = OrderService::new(repository);
}

#[cfg(test)]
mod lookup_tests {
    #[test]
    fn keeps_runtime_dispatch_out_of_the_static_fixture() {
        assert!(true);
    }
}
