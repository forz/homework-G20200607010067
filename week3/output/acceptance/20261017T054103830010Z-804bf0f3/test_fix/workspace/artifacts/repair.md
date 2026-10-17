# Test repair

The initial add implementation subtracted the right operand, so positive and
negative addition cases failed while the zero case happened to pass. The repair
changes the implementation to left + right. Existing tests are preserved exactly;
they check positive, negative, and zero operands. The configured unit command is
rerun after this report so its result is bound to the final workspace version.
