// SPDX-License-Identifier: MIT
pragma solidity 0.8.29;

contract HelloWorld {
    string private greeting;
    
    // Constructor to set initial greeting
    constructor(string memory _greeting) {
        greeting = _greeting;
    }
    
    // Function to get the current greeting
    function getGreeting() public view returns (string memory) {
        return greeting;
    }
    
    // Function to update the greeting
    function setGreeting(string memory _greeting) public {
        greeting = _greeting;
    }
}