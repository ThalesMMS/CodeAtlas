"""Order domain values."""

from dataclasses import dataclass
from enum import Enum


class OrderState(str, Enum):
    """Lifecycle of an order."""

    PENDING = "pending"
    CONFIRMED = "confirmed"


@dataclass(frozen=True)
class Order:
    """Value submitted to the order repository."""

    id: str
    state: OrderState = OrderState.PENDING
