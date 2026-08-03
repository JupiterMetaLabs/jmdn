// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

/**
 * @title HelloWorld
 * @dev Simple contract for testing compilation and execution
 */
contract HelloWorld {
    string private message;
    address public owner;
    uint256 public updateCount;

    event MessageUpdated(string newMessage, address updatedBy);

    constructor() {
        message = "Hello, World!";
        owner = msg.sender;
        updateCount = 0;
    }

    function getMessage() public view returns (string memory) {
        return message;
    }

    function setMessage(string memory _newMessage) public {
        message = _newMessage;
        updateCount++;
        emit MessageUpdated(_newMessage, msg.sender);
    }

    function getOwner() public view returns (address) {
        return owner;
    }

    function getUpdateCount() public view returns (uint256) {
        return updateCount;
    }
}
