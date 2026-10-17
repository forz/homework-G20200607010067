import pytest

from calculator import add, divide


def test_add():
    assert add(2, 3) == 5
    assert add(-2, 2) == 0


def test_divide():
    assert divide(9, 2) == 4.5


def test_divide_by_zero():
    with pytest.raises(ValueError, match="denominator"):
        divide(1, 0)
