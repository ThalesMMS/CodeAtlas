"""Repository contracts and an in-memory implementation."""

from typing import Protocol

from .models import Order


class OrderRepository(Protocol):
    async def save(self, order: Order) -> None:
        """Persist an order."""
        ...


class MemoryOrderRepository(OrderRepository):
    """Stores orders without executing external code."""

    def __init__(self) -> None:
        self._orders: dict[str, Order] = {}

    @property
    def count(self) -> int:
        return len(self._orders)

    async def save(self, order: Order) -> None:
        self._orders[order.id] = order

    async def find(self, order_id: str) -> Order | None:
        return self._orders.get(order_id)
