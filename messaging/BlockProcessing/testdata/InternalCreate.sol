// SPDX-License-Identifier: MIT
// Fixtures for the internal-account-creation apply-gate tests
// (messaging/BlockProcessing/internal_create_gate_test.go). Compiled with
//   solc 0.8.24 --evm-version shanghai --optimize --bin InternalCreate.sol
// and embedded as hex in internal_create_fixtures_test.go.
pragma solidity ^0.8.20;

// Forward pays a value-forwarding internal CALL to an arbitrary address.
contract Forward {
    function pay(address to) external payable {
        (bool ok, ) = payable(to).call{value: msg.value}("");
        require(ok, "pay");
    }

    function payTwo(address a, address b) external payable {
        uint256 h = msg.value / 2;
        (bool o1, ) = payable(a).call{value: h}("");
        (bool o2, ) = payable(b).call{value: msg.value - h}("");
        require(o1 && o2, "payTwo");
    }

    // Pays from the contract's OWN balance (intra-block dependency fixture).
    function payout(address to, uint256 amount) external {
        (bool ok, ) = payable(to).call{value: amount}("");
        require(ok, "payout");
    }

    receive() external payable {}
}

contract Child {
    constructor() payable {}
}

contract Factory {
    function create() external payable returns (address c) {
        c = address(new Child{value: msg.value}());
    }

    function create2(bytes32 salt) external payable returns (address c) {
        c = address(new Child{value: msg.value, salt: salt}());
    }
}

contract Bomb {
    constructor() payable {}

    function boom(address payable beneficiary) external {
        selfdestruct(beneficiary);
    }
}

// SuperJ-shaped: DIDRegistry -> SettlementContract.transferReward inside try/catch.
contract Settlement {
    constructor() payable {}

    function transferReward(address to, uint256 amount) external {
        (bool ok, ) = payable(to).call{value: amount}("");
        require(ok, "transfer");
    }
}

contract Registry {
    Settlement public immutable settlement;
    event WelcomePaid(address indexed wallet, uint256 amount);

    constructor(Settlement s) {
        settlement = s;
    }

    function register(address wallet, uint256 amount) external {
        try settlement.transferReward(wallet, amount) {
            emit WelcomePaid(wallet, amount);
        } catch {}
    }
}
