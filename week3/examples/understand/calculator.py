"""Small arithmetic module used as a trusted acceptance workspace."""


def add(left: int, right: int) -> int:
    return left + right


def divide(numerator: float, denominator: float) -> float:
    if denominator == 0:
        raise ValueError("denominator must not be zero")
    return numerator / denominator
