"""unittest-shaped declarations; CodeAtlas only indexes their structure."""

import unittest

from commerce.models import Order


class OrderModelTests(unittest.TestCase):
    def test_keeps_the_identifier(self) -> None:
        order = Order(id="order-2")
        self.assertEqual(order.id, "order-2")
