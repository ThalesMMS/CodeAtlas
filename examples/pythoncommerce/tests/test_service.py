"""Pytest-shaped declarations; CodeAtlas never executes them."""

import pytest

from commerce.models import Order
from commerce.repository import MemoryOrderRepository
from commerce.service import OrderService


@pytest.mark.asyncio
async def test_persists_an_order() -> None:
    repository = MemoryOrderRepository()
    service = OrderService(repository)
    await service.submit(Order(id="order-1"))
    assert await repository.find("order-1") is not None
