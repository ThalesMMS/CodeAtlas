"""Application service with conservative static call evidence."""

from collections.abc import Awaitable, Callable

from commerce.models import Order
from commerce.repository import OrderRepository


def traced(function: Callable[..., Awaitable[None]]) -> Callable[..., Awaitable[None]]:
    """Decorator syntax is indexed; its runtime transformation stays unknown."""
    return function


def validate_order(order: Order) -> None:
    if not order.id:
        raise ValueError("order id is required")


class OrderService:
    def __init__(self, repository: OrderRepository) -> None:
        self._repository = repository

    @traced
    async def submit(self, order: Order) -> None:
        validate_order(order)
        await self._repository.save(order)
